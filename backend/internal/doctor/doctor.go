// Package doctor runs AO's local health checks: the data dir, the tooling, the
// agent harnesses, and the credentials a machine needs before it can run
// sessions.
//
// It is the single implementation behind both `ao doctor` and the daemon's
// GET /api/v1/doctor, so a machine's readiness reads the same locally and
// remotely. The CLI formats the checks as text or JSON; the HTTP controller
// projects them onto a wire DTO. Neither owns the logic.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Level is a check outcome. FAIL is reserved for a broken machine: `ao doctor`
// exits non-zero on one, so a merely degraded state (a missing optional tool,
// a harness that is not signed in) is a WARN.
type Level string

// The three outcomes a check can report.
const (
	Pass Level = "PASS"
	Warn Level = "WARN"
	Fail Level = "FAIL"
)

// Check is one health check outcome. The JSON tags are the wire shape of
// `ao doctor --json`; the daemon's HTTP route projects this onto its own DTO
// rather than serializing it directly.
type Check struct {
	Level   Level  `json:"level"`
	Section string `json:"section,omitempty"`
	Name    string `json:"name"`
	Message string `json:"message"`
	// Remediation is the one command that fixes this check, when a single
	// command does. It exists so a consumer of `ao doctor --json` (the
	// desktop's machine card) can show exactly what to run instead of parsing
	// it back out of Message. Empty when there is no single command to name.
	Remediation string `json:"remediation,omitempty"`
	// PublicMessage replaces Message for a consumer off this machine, when
	// Message says more than a remote caller should be told. It is deliberately
	// not serialized: `ao doctor` and `ao doctor --json` run locally and keep
	// the full Message, and the daemon's HTTP route (a body that crosses the
	// network, see httpd/controllers.doctorReport) projects this instead when
	// it is set. Empty means Message is safe to publish as-is.
	PublicMessage string `json:"-"`
}

// Section names group the checks in a report, and the names and probe
// arguments the CLI and this package must agree on.
const (
	SectionCore   = "Core"
	SectionTools  = "Tools"
	SectionAgents = "Agent harnesses"
	SectionGitHub = "GitHub"
	SectionGitLab = "GitLab"

	// ClaudeHarnessName is the harness whose login `ao vm setup-harness` runs
	// and whose readiness the claude-auth check reports. Both halves read it
	// from here so they cannot drift apart.
	ClaudeHarnessName = "claude"

	// HooksLogName is the file under the data dir where the CLI appends hook
	// delivery failures (see cli.appendHooksLog). The writer and the
	// hooks-log check below must agree on the name, and the CLI imports this
	// package, so the name lives here.
	HooksLogName = "hooks.log"

	// DefaultGitHubRESTBase is the API root the github-token check probes.
	DefaultGitHubRESTBase = "https://api.github.com"

	// DefaultGitLabRESTBase is the API root the gitlab-token check probes.
	DefaultGitLabRESTBase = "https://gitlab.com/api/v4"

	minGitVersion         = "2.25.0"
	githubDoctorUserAgent = "ao-agent-orchestrator/doctor"
	gitlabDoctorUserAgent = "ao-agent-orchestrator/doctor"
	probeTimeout          = 2 * time.Second

	// harnessAuthTimeout is the claude-auth probe's own budget. probeTimeout is
	// sized for `git --version`: a local binary that prints a line. `claude auth
	// status --json` is a Node CLI cold start, which on a 1 vCPU VM routinely
	// takes longer than that, and it may talk to the network. Timing out there
	// produces a WARN that the desktop maps to "harness not set up", so too
	// small a budget makes a correctly signed-in machine report as unconfigured
	// and sends the user back to `ao vm setup-harness`.
	harnessAuthTimeout = 10 * time.Second

	// maxProbeOutputInMessage caps how much harness output a check message may
	// quote. These messages cross the network on GET /api/v1/doctor, so a
	// future harness release that prints something long, or something private,
	// on this path cannot turn the report into a transcript of it.
	maxProbeOutputInMessage = 200
)

// ClaudeAuthStatusArgs is the harness's own machine-readable answer to "is
// this machine signed in", and the only probe the claude-auth check runs.
var ClaudeAuthStatusArgs = []string{"auth", "status", "--json"}

type harnessProbe struct {
	Name       string
	BinaryName string
	VersionArg string
	// ExpectedVersionPrefix, when set, is the prefix the harness's version
	// output must start with to be trusted as that harness. Some CLIs (muse)
	// share a binary name convention loosely enough that a same-named but
	// unrelated binary on PATH would otherwise pass silently.
	ExpectedVersionPrefix string
}

var harnesses = []harnessProbe{
	{Name: "claude-code", BinaryName: ClaudeHarnessName, VersionArg: "--version"},
	{Name: "codex", BinaryName: "codex", VersionArg: "--version"},
	{Name: "muse", BinaryName: "muse", VersionArg: "--version", ExpectedVersionPrefix: "Muse Code "},
}

// Deps holds the side effects the checks need, so both callers can inject
// fakes instead of reaching into process-global state.
type Deps struct {
	LookPath      func(file string) (string, error)
	CommandOutput func(ctx context.Context, name string, args ...string) ([]byte, error)
	Executable    func() (string, error)
	HTTPClient    *http.Client
	// GitHubRESTBase lets tests point the github-token probe at httptest.
	GitHubRESTBase string
	// GitLabRESTBase lets tests point the gitlab-token probe at httptest.
	GitLabRESTBase string
	// DaemonCheck reports the daemon's own state. It is caller-supplied
	// because the answer differs by vantage point: the CLI inspects the run
	// file and probes the loopback health endpoints, while the daemon answers
	// for itself. A nil DaemonCheck omits the check entirely.
	DaemonCheck func(ctx context.Context) Check
}

// DefaultDeps returns production dependencies.
func DefaultDeps() Deps {
	return Deps{
		LookPath:       exec.LookPath,
		CommandOutput:  commandOutput,
		Executable:     os.Executable,
		HTTPClient:     &http.Client{Timeout: probeTimeout},
		GitHubRESTBase: DefaultGitHubRESTBase,
		GitLabRESTBase: DefaultGitLabRESTBase,
	}
}

// commandOutput runs a probe through the shared helper, which bounds the wait
// on the output pipes (aoprocess.WaitDelay). A probe that ignored that would
// hang the whole report, and now the HTTP request behind it, whenever a
// grandchild outlives the probe's context: `claude` is a Node CLI and spawns
// exactly such children.
func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return aoprocess.CombinedOutput(ctx, name, args...)
}

func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.LookPath == nil {
		d.LookPath = def.LookPath
	}
	if d.CommandOutput == nil {
		d.CommandOutput = def.CommandOutput
	}
	if d.Executable == nil {
		d.Executable = def.Executable
	}
	if d.HTTPClient == nil {
		d.HTTPClient = def.HTTPClient
	}
	if d.GitHubRESTBase == "" {
		d.GitHubRESTBase = def.GitHubRESTBase
	}
	if d.GitLabRESTBase == "" {
		d.GitLabRESTBase = def.GitLabRESTBase
	}
	return d
}

// Run executes every check and returns them in report order. It never returns
// an error: a check that cannot run reports itself as a failing check, which
// is the whole point of a doctor.
func Run(ctx context.Context, deps Deps) []Check {
	d := deps.withDefaults()
	checks := []Check{}

	cfg, err := config.Load()
	if err != nil {
		return append(checks, Check{Level: Fail, Section: SectionCore, Name: "config", Message: err.Error()})
	}
	checks = append(checks, Check{
		Level: Pass, Section: SectionCore, Name: "config",
		Message: fmt.Sprintf("runFile=%s dataDir=%s port=%d", cfg.RunFilePath, cfg.DataDir, cfg.Port),
	})

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		checks = append(checks, Check{Level: Fail, Section: SectionCore, Name: "data-dir", Message: err.Error()})
	} else {
		checks = append(checks,
			Check{Level: Pass, Section: SectionCore, Name: "data-dir", Message: cfg.DataDir},
			checkDataDirWritable(cfg.DataDir),
		)
	}

	checks = append(checks, checkStore(cfg.DataDir), checkHooksLog(cfg.DataDir, time.Now()))

	if d.DaemonCheck != nil {
		checks = append(checks, d.DaemonCheck(ctx))
	}

	checks = append(checks,
		d.checkGit(ctx),
		d.checkTerminalRuntime(ctx),
		d.checkAOBinary(),
	)
	for _, harness := range harnesses {
		checks = append(checks, d.checkHarness(ctx, harness))
	}
	checks = append(checks, d.checkClaudeAuth(ctx), d.checkCodexLaunchFlags(ctx), d.checkGitHubToken(ctx), d.checkGitLabToken(ctx))
	return checks
}

// checkClaudeAuth reports whether the claude harness has finished its own
// login on this machine. `claude auth status --json` is the harness's own
// machine-readable answer, so this reports the harness's state rather than
// guessing at where its credentials live (a keychain entry on macOS, a file
// elsewhere, or an environment variable). It is the readiness signal the
// desktop reads for a machine card: a machine that is registered but has no
// harness configured shows Remediation instead of failing silently.
//
// A missing login is a WARN, never a FAIL: plenty of machines run AO with a
// different harness, or with none, and `ao doctor` must still exit 0 there.
func (d Deps) checkClaudeAuth(ctx context.Context) Check {
	const name = "claude-auth"
	setupCmd := "ao vm setup-harness " + ClaudeHarnessName
	warn := func(message string) Check {
		return Check{
			Level: Warn, Section: SectionAgents, Name: name,
			Message: message, Remediation: setupCmd,
		}
	}

	path, err := d.LookPath(ClaudeHarnessName)
	if err != nil || path == "" {
		return warn(fmt.Sprintf("claude not found in PATH; install the Claude Code CLI, then run `%s`", setupCmd))
	}
	reqCtx, cancel := context.WithTimeout(ctx, harnessAuthTimeout)
	defer cancel()
	probe := strings.Join(ClaudeAuthStatusArgs, " ")
	out, cmdErr := d.CommandOutput(reqCtx, path, ClaudeAuthStatusArgs...)
	// `claude auth status` exits non-zero when the harness is not logged in and
	// still prints its JSON, so the output is what answers the question. A
	// non-zero exit only matters when there is no parseable answer in it.
	status, parseErr := parseClaudeAuthStatus(out)
	if parseErr != nil {
		reason := parseErr
		if cmdErr != nil {
			reason = cmdErr
		}
		return warn(fmt.Sprintf("could not read harness auth state (`claude %s`: %v); log in with `%s`", probe, reason, setupCmd))
	}
	if !status.LoggedIn {
		return warn(fmt.Sprintf("%s is installed but not signed in; run `%s`", path, setupCmd))
	}
	return Check{
		Level: Pass, Section: SectionAgents, Name: name,
		Message: fmt.Sprintf("%s is signed in (%s)", path, status.describe()),
	}
}

type claudeAuthStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	AuthMethod  string `json:"authMethod"`
	APIProvider string `json:"apiProvider"`
}

func (s claudeAuthStatus) describe() string {
	method := strings.TrimSpace(s.AuthMethod)
	if method == "" {
		method = "unknown"
	}
	if provider := strings.TrimSpace(s.APIProvider); provider != "" {
		return fmt.Sprintf("authMethod=%s apiProvider=%s", method, provider)
	}
	return "authMethod=" + method
}

// parseClaudeAuthStatus reads the JSON object out of `claude auth status
// --json` output. It slices to the outermost braces first because the probe
// runs through CombinedOutput, so an unrelated notice on stderr (an update
// banner, a deprecation warning) would otherwise make valid JSON unparseable.
//
// The "no JSON" error quotes only a truncated head of that output: the error
// becomes the check Message, which GET /api/v1/doctor serves, so whatever a
// future claude release decides to print must not be echoed wholesale.
func parseClaudeAuthStatus(out []byte) (claudeAuthStatus, error) {
	clean := ansiRE.ReplaceAllString(string(out), "")
	start, end := strings.Index(clean, "{"), strings.LastIndex(clean, "}")
	if start < 0 || end < start {
		return claudeAuthStatus{}, fmt.Errorf("no JSON object in output %q", truncate(strings.TrimSpace(clean), maxProbeOutputInMessage))
	}
	var status claudeAuthStatus
	if err := json.Unmarshal([]byte(clean[start:end+1]), &status); err != nil {
		return claudeAuthStatus{}, err
	}
	return status, nil
}

// checkStore inspects the SQLite store WITHOUT opening or migrating it. The
// daemon is the sole writer and migrator of the database (architecture.md §7);
// the CLI must never run migrations or open a second writer against a database
// a live daemon may already own. Migrations are validated by the daemon at
// startup and surfaced through /readyz, so doctor only confirms whether the
// database file exists yet.
func checkStore(dataDir string) Check {
	dbPath := filepath.Join(dataDir, "ao.db")
	info, err := os.Stat(dbPath)
	switch {
	case err == nil:
		return Check{
			Level: Pass, Section: SectionCore, Name: "sqlite",
			Message: fmt.Sprintf("%s (%d bytes); migrations are applied by the daemon at startup", dbPath, info.Size()),
		}
	case errors.Is(err, fs.ErrNotExist):
		return Check{
			Level: Warn, Section: SectionCore, Name: "sqlite",
			Message: "database not created yet; run `ao start` to initialize and migrate it",
		}
	default:
		return Check{Level: Fail, Section: SectionCore, Name: "sqlite", Message: err.Error()}
	}
}

func checkDataDirWritable(dataDir string) Check {
	f, err := os.CreateTemp(dataDir, ".ao-doctor-write-*")
	if err != nil {
		return Check{Level: Fail, Section: SectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	name := f.Name()
	if _, err := f.WriteString("ok\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return Check{Level: Fail, Section: SectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return Check{Level: Fail, Section: SectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	if err := os.Remove(name); err != nil {
		return Check{Level: Warn, Section: SectionCore, Name: "data-dir-write", Message: fmt.Sprintf("write probe succeeded but cleanup failed: %v", err)}
	}
	return Check{Level: Pass, Section: SectionCore, Name: "data-dir-write", Message: "write probe succeeded"}
}

// checkAOBinary verifies the `ao` that workspace hooks would invoke. Agent
// adapters install hook commands as a bare `ao hooks <agent> <event>`, so an
// `ao` earlier on PATH that is not this binary (e.g. a legacy CLI without the
// hooks command) fails every callback and silently kills activity tracking.
// The daemon pins PATH inside the sessions it spawns, so a mismatch here is a
// warning about every other context (manual runs, foreign panes), not a hard
// failure.
func (d Deps) checkAOBinary() Check {
	const name = "ao-binary"
	self, err := d.Executable()
	if err != nil {
		return Check{Level: Warn, Section: SectionTools, Name: name, Message: fmt.Sprintf("could not resolve the running executable: %v", err)}
	}
	onPath, err := d.LookPath("ao")
	if err != nil || onPath == "" {
		return Check{
			Level: Warn, Section: SectionTools, Name: name,
			Message: "ao not found in PATH; workspace hooks invoke `ao hooks <agent> <event>` (daemon-spawned sessions pin PATH to the daemon binary and are unaffected)",
		}
	}
	if sameBinary(self, onPath) {
		return Check{Level: Pass, Section: SectionTools, Name: name, Message: fmt.Sprintf("ao in PATH is this binary (%s)", onPath)}
	}
	return Check{
		Level: Warn, Section: SectionTools, Name: name,
		Message: fmt.Sprintf("ao in PATH is %s, not this binary (%s); workspace hooks run `ao hooks` and a foreign ao breaks activity tracking outside daemon-spawned sessions", onPath, self),
	}
}

// sameBinary reports whether two paths name the same file, tolerating symlinks
// via os.SameFile and falling back to cleaned-path equality when either stat
// fails.
func sameBinary(a, b string) bool {
	ai, aErr := os.Stat(a)
	bi, bErr := os.Stat(b)
	if aErr == nil && bErr == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (d Deps) checkGit(ctx context.Context) Check {
	path, err := d.LookPath("git")
	if err != nil || path == "" {
		return Check{Level: Fail, Section: SectionTools, Name: "git", Message: "not found in PATH"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := d.CommandOutput(reqCtx, path, "--version")
	if err != nil {
		return Check{Level: Fail, Section: SectionTools, Name: "git", Message: fmt.Sprintf("%s: %v", path, err)}
	}
	version, err := parseGitVersion(string(out))
	if err != nil {
		return Check{Level: Warn, Section: SectionTools, Name: "git", Message: fmt.Sprintf("%s (version unknown: %s)", path, FirstOutputLine(out))}
	}
	cmp, err := compareDottedVersion(version, minGitVersion)
	if err != nil {
		return Check{Level: Warn, Section: SectionTools, Name: "git", Message: fmt.Sprintf("%s (version unknown: %s)", path, FirstOutputLine(out))}
	}
	if cmp < 0 {
		return Check{Level: Warn, Section: SectionTools, Name: "git", Message: fmt.Sprintf("%s (version %s; AO expects >= %s for worktrees)", path, version, minGitVersion)}
	}
	return Check{Level: Pass, Section: SectionTools, Name: "git", Message: fmt.Sprintf("%s (version %s; supports worktrees)", path, version)}
}

// checkTerminalRuntime checks the runtime multiplexer used on this platform:
// tmux on Darwin/Linux, ConPTY (built-in) on Windows.
func (d Deps) checkTerminalRuntime(ctx context.Context) Check {
	if runtime.GOOS == "windows" {
		return Check{
			Level:   Pass,
			Section: SectionTools,
			Name:    "conpty",
			Message: "ConPTY (built-in): no external terminal multiplexer required on Windows",
		}
	}
	return d.checkTmux(ctx)
}

func (d Deps) checkTmux(ctx context.Context) Check {
	path, err := d.LookPath("tmux")
	if err != nil || path == "" {
		return Check{Level: Warn, Section: SectionTools, Name: "tmux", Message: "not found in PATH; required on macOS/Linux to start sessions"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := d.CommandOutput(reqCtx, path, "-V")
	if err != nil {
		return Check{Level: Fail, Section: SectionTools, Name: "tmux", Message: fmt.Sprintf("%s: %v", path, err)}
	}
	version := FirstOutputLine(out)
	if version == "" {
		version = "version unknown"
	}
	return Check{Level: Pass, Section: SectionTools, Name: "tmux", Message: fmt.Sprintf("%s (%s)", path, version)}
}

// checkHooksLog surfaces recent agent hook delivery failures. `ao hooks`
// callbacks deliberately swallow errors (a hook must never break the user's
// agent), so $AO_DATA_DIR/hooks.log is the only place a dead activity feed
// becomes visible. Lines start with an RFC3339 timestamp (see appendHooksLog).
func checkHooksLog(dataDir string, now time.Time) Check {
	const name = "hooks-log"
	path := filepath.Join(dataDir, HooksLogName)
	data, err := os.ReadFile(path) //nolint:gosec // path rooted in AO's own data dir
	if errors.Is(err, fs.ErrNotExist) {
		return Check{Level: Pass, Section: SectionCore, Name: name, Message: "no hook delivery failures recorded"}
	}
	if err != nil {
		return Check{Level: Warn, Section: SectionCore, Name: name, Message: err.Error()}
	}

	recent := 0
	latest := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		stamp, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339, stamp)
		if err != nil || now.Sub(ts) > 24*time.Hour {
			continue
		}
		recent++
		latest = line
	}
	if recent == 0 {
		return Check{Level: Pass, Section: SectionCore, Name: name, Message: fmt.Sprintf("no hook delivery failures in the last 24h (%s)", path)}
	}
	return Check{
		Level: Warn, Section: SectionCore, Name: name,
		Message: fmt.Sprintf("%d hook delivery failure(s) in the last 24h — activity tracking may be degraded; latest: %s (full log: %s)", recent, latest, path),
	}
}

func (d Deps) checkHarness(ctx context.Context, harness harnessProbe) Check {
	path, err := d.LookPath(harness.BinaryName)
	if err != nil || path == "" {
		return Check{
			Level: Warn, Section: SectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s not found in PATH", harness.BinaryName),
		}
	}
	if harness.VersionArg == "" {
		return Check{Level: Pass, Section: SectionAgents, Name: harness.Name, Message: fmt.Sprintf("%s resolves to %s", harness.BinaryName, path)}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := d.CommandOutput(reqCtx, path, harness.VersionArg)
	if err != nil {
		return Check{
			Level: Warn, Section: SectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s resolves to %s, but `%s %s` failed: %v", harness.BinaryName, path, harness.BinaryName, harness.VersionArg, err),
		}
	}
	version := FirstOutputLine(out)
	if version == "" {
		version = "version output was empty"
	}
	if harness.ExpectedVersionPrefix != "" && !strings.HasPrefix(version, harness.ExpectedVersionPrefix) {
		return Check{
			Level: Warn, Section: SectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s resolves to %s, but its version output %q does not identify the expected CLI (%q prefix)", harness.BinaryName, path, version, harness.ExpectedVersionPrefix),
		}
	}
	return Check{Level: Pass, Section: SectionAgents, Name: harness.Name, Message: fmt.Sprintf("%s resolves to %s (%s)", harness.BinaryName, path, version)}
}

// checkCodexLaunchFlags smoke-tests AO's codex launch surface against the
// installed binary: the hook-trust bypass flag and the `-c` session-flag
// config AO injects at spawn (activity hooks, worktree trust, nudge
// suppression). Codex has no stable hook-config contract, so a codex upgrade
// can silently break activity tracking; this canary turns that breakage into
// a doctor warning. The probes come from the codex adapter itself so they
// cannot drift from the real spawn argv.
func (d Deps) checkCodexLaunchFlags(ctx context.Context) Check {
	const name = "codex-launch-flags"
	path, err := d.LookPath("codex")
	if err != nil || path == "" {
		return Check{Level: Pass, Section: SectionAgents, Name: name, Message: "skipped: codex not found in PATH"}
	}
	for _, probe := range codex.DoctorLaunchProbes() {
		reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		out, err := d.CommandOutput(reqCtx, path, probe...)
		cancel()
		if err != nil {
			return Check{
				Level: Warn, Section: SectionAgents, Name: name,
				Message: fmt.Sprintf("codex rejected AO's launch flags (`codex %s`: %v) — codex sessions may spawn without activity hooks; a codex CLI update likely changed its flag/config surface", strings.Join(probe, " "), err),
			}
		}
		if strings.Contains(string(out), "unknown configuration field") {
			return Check{
				Level: Warn, Section: SectionAgents, Name: name,
				Message: fmt.Sprintf("codex no longer recognizes one of AO's config overrides (%s) — codex sessions may spawn without activity hooks", FirstOutputLine(out)),
			}
		}
	}
	return Check{Level: Pass, Section: SectionAgents, Name: name, Message: "codex accepts AO's hook/trust launch flags"}
}

func (d Deps) checkGitHubToken(ctx context.Context) Check {
	token, source, err := d.githubToken(ctx)
	if err != nil {
		return Check{Level: Warn, Section: SectionGitHub, Name: "github-token", Message: err.Error()}
	}

	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(d.GitHubRESTBase, "/")+"/user", http.NoBody)
	if err != nil {
		return Check{Level: Fail, Section: SectionGitHub, Name: "github-token", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubDoctorUserAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Check{Level: Fail, Section: SectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token validation failed: %v", source, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Check{Level: Fail, Section: SectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token rejected by GitHub (HTTP %d)", source, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Check{Level: Warn, Section: SectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token probe returned HTTP %d", source, resp.StatusCode)}
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return Check{Level: Fail, Section: SectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token probe decode failed: %v", source, err)}
	}
	login := user.Login
	if login == "" {
		login = "unknown user"
	}
	scopes := strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
	scopeMsg := "scopes unavailable"
	if scopes != "" {
		scopeMsg = "scopes: " + scopes
	}
	return Check{
		Level: Pass, Section: SectionGitHub, Name: "github-token",
		Message: fmt.Sprintf("%s token valid for %s (%s)", source, login, scopeMsg),
		// The login names the GitHub account this machine acts as and the scope
		// list is the exact capability of the credential sitting on it. Both are
		// what you want locally and neither is anyone else's business, so the
		// remote projection gets the answer to the question only: is the token
		// good.
		PublicMessage: fmt.Sprintf("%s token valid", source),
	}
}

func (d Deps) checkGitLabToken(ctx context.Context) Check {
	token, source, err := d.gitlabToken(ctx)
	if err != nil {
		return Check{Level: Warn, Section: SectionGitLab, Name: "gitlab-token", Message: err.Error()}
	}

	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(d.GitLabRESTBase, "/")+"/user", http.NoBody)
	if err != nil {
		return Check{Level: Fail, Section: SectionGitLab, Name: "gitlab-token", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", gitlabDoctorUserAgent)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Check{Level: Fail, Section: SectionGitLab, Name: "gitlab-token", Message: fmt.Sprintf("%s token validation failed: %v", source, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Check{Level: Fail, Section: SectionGitLab, Name: "gitlab-token", Message: fmt.Sprintf("%s token rejected by GitLab (HTTP %d)", source, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Check{Level: Warn, Section: SectionGitLab, Name: "gitlab-token", Message: fmt.Sprintf("%s token probe returned HTTP %d", source, resp.StatusCode)}
	}

	var user struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return Check{Level: Fail, Section: SectionGitLab, Name: "gitlab-token", Message: fmt.Sprintf("%s token probe decode failed: %v", source, err)}
	}
	login := user.Username
	if login == "" {
		login = "unknown user"
	}
	return Check{
		Level: Pass, Section: SectionGitLab, Name: "gitlab-token",
		Message: fmt.Sprintf("%s token valid for %s", source, login),
		// Same reasoning as github-token: the username names the account this
		// machine acts as, which is nobody else's business off the box.
		PublicMessage: fmt.Sprintf("%s token valid", source),
	}
}

func (d Deps) gitlabToken(ctx context.Context) (token, source string, err error) {
	for _, name := range []string{"AO_GITLAB_TOKEN", "GITLAB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, name, nil
		}
	}
	path, lookErr := d.LookPath("glab")
	if lookErr != nil || path == "" {
		return "", "", errors.New("no GitLab token found (set AO_GITLAB_TOKEN/GITLAB_TOKEN or run `glab auth login`)")
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, cmdErr := d.CommandOutput(reqCtx, path, "auth", "status", "--show-token")
	if cmdErr != nil {
		return "", "", fmt.Errorf("glab is installed but no token was available (`glab auth status --show-token` failed: %w)", cmdErr)
	}
	token = parseGLabTokenLine(string(out))
	if token == "" {
		return "", "", errors.New("glab is installed but returned no auth token")
	}
	return token, "glab", nil
}

// parseGLabTokenLine extracts the token value from `glab auth status --show-token`
// output. The token appears on a line containing "Token" followed by a colon
// and the token value (e.g. "Token found: glpat-xxx"). This mirrors the
// parsing logic in the GitLab SCM adapter (gitlab/auth.go) without importing
// the adapter package here.
func parseGLabTokenLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		tokenIdx := strings.Index(line, "Token")
		if tokenIdx < 0 {
			continue
		}
		colonIdx := strings.Index(line[tokenIdx:], ":")
		if colonIdx < 0 {
			continue
		}
		val := strings.TrimSpace(line[tokenIdx+colonIdx+1:])
		if val != "" {
			return val
		}
	}
	return ""
}

// truncate shortens s to at most maxRunes characters and says that it did.
// Rune-based so a cut never lands mid-sequence and produces mojibake.
func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "... (truncated)"
}

func (d Deps) githubToken(ctx context.Context) (token, source string, err error) {
	for _, name := range []string{"AO_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, name, nil
		}
	}
	path, lookErr := d.LookPath("gh")
	if lookErr != nil || path == "" {
		return "", "", errors.New("no GitHub token found (set AO_GITHUB_TOKEN/GITHUB_TOKEN or run `gh auth login`)")
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, cmdErr := d.CommandOutput(reqCtx, path, "auth", "token")
	if cmdErr != nil {
		return "", "", fmt.Errorf("gh is installed but no token was available (`gh auth token` failed: %w)", cmdErr)
	}
	token = strings.TrimSpace(string(out))
	if token == "" {
		return "", "", errors.New("gh is installed but returned an empty auth token")
	}
	return token, "gh", nil
}

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	gitVersionRE = regexp.MustCompile(`(?i)\bgit version\s+(\d+(?:\.\d+){1,3})`)
)

func parseGitVersion(out string) (string, error) {
	clean := ansiRE.ReplaceAllString(out, "")
	m := gitVersionRE.FindStringSubmatch(clean)
	if len(m) < 2 {
		return "", fmt.Errorf("parse git version from %q", strings.TrimSpace(clean))
	}
	return m[1], nil
}

// FirstOutputLine returns the first non-empty line of command output with ANSI
// escapes stripped. It is exported because the CLI's VM preflight reports
// probed tool versions the same way.
func FirstOutputLine(out []byte) string {
	clean := strings.TrimSpace(ansiRE.ReplaceAllString(string(out), ""))
	if clean == "" {
		return ""
	}
	line := strings.SplitN(clean, "\n", 2)[0]
	return strings.TrimSpace(line)
}

func compareDottedVersion(a, b string) (int, error) {
	ap, err := dottedVersionParts(a)
	if err != nil {
		return 0, err
	}
	bp, err := dottedVersionParts(b)
	if err != nil {
		return 0, err
	}
	maxLen := len(ap)
	if len(bp) > maxLen {
		maxLen = len(bp)
	}
	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		switch {
		case av < bv:
			return -1, nil
		case av > bv:
			return 1, nil
		}
	}
	return 0, nil
}

func dottedVersionParts(s string) ([]int, error) {
	raw := strings.Split(s, ".")
	parts := make([]int, 0, len(raw))
	for _, part := range raw {
		if part == "" {
			return nil, fmt.Errorf("empty version segment in %q", s)
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("parse version segment %q in %q: %w", part, s, err)
		}
		parts = append(parts, n)
	}
	return parts, nil
}
