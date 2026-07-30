package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestChecksGitVersion(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/git" || len(args) != 1 || args[0] != "--version" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return []byte("git version 2.43.0\n"), nil
	})

	check := findCheck(t, Run(context.Background(), deps), "git")
	if check.Level != Pass || !strings.Contains(check.Message, "2.43.0") || !strings.Contains(check.Message, "supports worktrees") {
		t.Fatalf("git check = %+v, want PASS with version", check)
	}
}

func TestWarnsOnUnsupportedGitVersion(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.24.9\n"), nil
	})

	check := findCheck(t, Run(context.Background(), deps), "git")
	if check.Level != Warn || !strings.Contains(check.Message, ">= 2.25.0") {
		t.Fatalf("git check = %+v, want WARN with minimum version", check)
	}
}

func TestFailsWhenGitMissing(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{}, nil)

	check := findCheck(t, Run(context.Background(), deps), "git")
	if check.Level != Fail {
		t.Fatalf("git check = %+v, want FAIL", check)
	}
}

func TestChecksTmuxVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "tmux": "/bin/tmux"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case "/bin/tmux":
			if len(args) != 1 || args[0] != "-V" {
				t.Fatalf("unexpected tmux command: %s %v", name, args)
			}
			return []byte("tmux 3.3a\n"), nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	})

	check := findCheck(t, Run(context.Background(), deps), "tmux")
	if check.Level != Pass || !strings.Contains(check.Message, "3.3a") {
		t.Fatalf("tmux check = %+v, want PASS with version", check)
	}
}

// TestChecksTmuxVersionFailsOnError covers the case where tmux is found but
// the version command fails.
func TestChecksTmuxVersionFailsOnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "tmux": "/bin/tmux"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/git" {
			return []byte("git version 2.43.0\n"), nil
		}
		return nil, errors.New("exec: tmux: not found")
	})

	check := findCheck(t, Run(context.Background(), deps), "tmux")
	if check.Level != Fail {
		t.Fatalf("tmux check = %+v, want FAIL on version error", check)
	}
}

func TestWarnsWhenTmuxMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	check := findCheck(t, Run(context.Background(), deps), "tmux")
	if check.Level != Warn {
		t.Fatalf("tmux check = %+v, want WARN", check)
	}
}

func TestChecksHarnessVersions(t *testing.T) {
	setConfigEnv(t)
	cmdPath := map[string]string{
		"git":    "/bin/git",
		"claude": "/bin/claude",
		"codex":  "/bin/codex",
	}
	deps := testDeps(t, cmdPath, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case "/bin/claude", "/bin/codex":
			if len(args) == 1 && args[0] == "--version" {
				return []byte(strings.TrimPrefix(name, "/bin/") + " 1.2.3\n"), nil
			}
			// The claude-auth readiness check probes the same binary.
			if name == "/bin/claude" && strings.Join(args, " ") == "auth status --json" {
				return []byte(`{"loggedIn":true,"authMethod":"claudeai","apiProvider":"firstParty"}`), nil
			}
			// The codex launch-flag canary probes the same binary.
			if name == "/bin/codex" && len(args) > 0 && (args[0] == "--dangerously-bypass-hook-trust" || args[0] == "features") {
				return []byte("ok\n"), nil
			}
			t.Fatalf("unexpected harness command: %s %v", name, args)
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	})

	checks := Run(context.Background(), deps)
	for _, name := range []string{"claude-code", "codex"} {
		check := findCheck(t, checks, name)
		if check.Level != Pass || !strings.Contains(check.Message, "resolves to") {
			t.Fatalf("%s check = %+v, want PASS with path/version", name, check)
		}
	}
}

func TestWarnsWhenHarnessMissing(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	check := findCheck(t, Run(context.Background(), deps), "codex")
	if check.Level != Warn || !strings.Contains(check.Message, "not found in PATH") {
		t.Fatalf("codex check = %+v, want WARN missing binary", check)
	}
}

func TestWarnsWhenHarnessVersionFails(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/git" {
			return []byte("git version 2.43.0\n"), nil
		}
		return nil, errors.New("boom")
	})

	check := findCheck(t, Run(context.Background(), deps), "codex")
	if check.Level != Warn || !strings.Contains(check.Message, "failed") {
		t.Fatalf("codex check = %+v, want WARN version failure", check)
	}
}

// claudeAuthFake answers the harness version probe and the claude-auth
// readiness probe against a fake claude at /bin/claude.
func claudeAuthFake(t *testing.T, statusOutput string, statusErr error) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case name == "/bin/claude" && strings.Join(args, " ") == "--version":
			return []byte("2.1.220 (Claude Code)\n"), nil
		case name == "/bin/claude" && strings.Join(args, " ") == "auth status --json":
			return []byte(statusOutput), statusErr
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	}
}

func TestClaudeAuthPassesWhenSignedIn(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "claude": "/bin/claude"},
		claudeAuthFake(t, `{"loggedIn":true,"authMethod":"claudeai","apiProvider":"firstParty"}`, nil))

	check := findCheck(t, Run(context.Background(), deps), "claude-auth")
	if check.Level != Pass {
		t.Fatalf("claude-auth check = %+v, want PASS", check)
	}
	if !strings.Contains(check.Message, "signed in") || !strings.Contains(check.Message, "authMethod=claudeai") {
		t.Errorf("claude-auth message = %q, want the auth method reported", check.Message)
	}
	if check.Remediation != "" {
		t.Errorf("claude-auth remediation = %q, want empty when the check passes", check.Remediation)
	}
}

// TestClaudeAuthWarnsWhenNotSignedIn is the state the desktop machine card
// renders: a machine with the harness installed but no login must name the
// exact command that fixes it rather than failing silently. The non-zero exit
// is real: `claude auth status --json` exits 1 when not logged in and still
// prints its JSON, so the output answers the question, not the exit code.
func TestClaudeAuthWarnsWhenNotSignedIn(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "claude": "/bin/claude"},
		claudeAuthFake(t, `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`, errors.New("exit status 1")))

	check := findCheck(t, Run(context.Background(), deps), "claude-auth")
	if check.Level != Warn || !strings.Contains(check.Message, "not signed in") {
		t.Fatalf("claude-auth check = %+v, want WARN not signed in", check)
	}
	if check.Remediation != "ao vm setup-harness claude" {
		t.Errorf("claude-auth remediation = %q, want the setup-harness command", check.Remediation)
	}
}

func TestClaudeAuthWarnsWhenHarnessMissing(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	check := findCheck(t, Run(context.Background(), deps), "claude-auth")
	if check.Level != Warn || !strings.Contains(check.Message, "not found in PATH") {
		t.Fatalf("claude-auth check = %+v, want WARN missing binary", check)
	}
	if check.Remediation != "ao vm setup-harness claude" {
		t.Errorf("claude-auth remediation = %q, want the setup-harness command", check.Remediation)
	}
}

// TestClaudeAuthWarnsWhenProbeUnusable covers a claude release that no longer
// answers `claude auth status --json`, and output that is not JSON at all.
// Neither may crash doctor or be reported as signed in.
func TestClaudeAuthWarnsWhenProbeUnusable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "command fails", output: "error: unknown command 'auth'\n", err: errors.New("exit status 1")},
		{name: "output is not json", output: "Logged in as octocat\n"},
		{name: "output is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfigEnv(t)
			deps := testDeps(t, map[string]string{"git": "/bin/git", "claude": "/bin/claude"},
				claudeAuthFake(t, tc.output, tc.err))

			check := findCheck(t, Run(context.Background(), deps), "claude-auth")
			if check.Level != Warn || !strings.Contains(check.Message, "could not read harness auth state") {
				t.Fatalf("claude-auth check = %+v, want WARN unreadable auth state", check)
			}
		})
	}
}

// TestParseClaudeAuthStatusIgnoresSurroundingNoise pins the reason the probe
// slices to the outermost braces: it runs through CombinedOutput, so a notice
// on stderr must not make valid JSON unparseable.
func TestParseClaudeAuthStatusIgnoresSurroundingNoise(t *testing.T) {
	out := []byte("\x1b[33mA new version of Claude Code is available.\x1b[0m\n" +
		`{"loggedIn":true,"authMethod":"apiKey","apiProvider":"firstParty"}` + "\n")
	status, err := parseClaudeAuthStatus(out)
	if err != nil {
		t.Fatalf("parseClaudeAuthStatus: %v", err)
	}
	if !status.LoggedIn || status.AuthMethod != "apiKey" {
		t.Fatalf("status = %+v, want loggedIn with authMethod=apiKey", status)
	}
	if got := status.describe(); got != "authMethod=apiKey apiProvider=firstParty" {
		t.Errorf("describe = %q", got)
	}
}

func TestChecksGitHubTokenFromEnv(t *testing.T) {
	setConfigEnv(t)
	srv := githubServer(t, http.StatusOK, `{"login":"octocat"}`, "repo, read:org")
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
	t.Setenv("AO_GITHUB_TOKEN", "env-token")
	deps.HTTPClient = srv.Client()
	deps.GitHubRESTBase = srv.URL

	check := findCheck(t, Run(context.Background(), deps), "github-token")
	if check.Level != Pass || !strings.Contains(check.Message, "AO_GITHUB_TOKEN") || !strings.Contains(check.Message, "repo, read:org") {
		t.Fatalf("github-token check = %+v, want PASS with source and scopes", check)
	}
}

func TestChecksGitHubTokenFromGHCLI(t *testing.T) {
	setConfigEnv(t)
	srv := githubServer(t, http.StatusOK, `{"login":"octocat"}`, "")
	deps := testDeps(t, map[string]string{"git": "/bin/git", "gh": "/bin/gh"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/gh" {
			if len(args) != 2 || args[0] != "auth" || args[1] != "token" {
				t.Fatalf("unexpected gh command: %s %v", name, args)
			}
			return []byte("gh-token\n"), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	deps.HTTPClient = srv.Client()
	deps.GitHubRESTBase = srv.URL

	check := findCheck(t, Run(context.Background(), deps), "github-token")
	if check.Level != Pass || !strings.Contains(check.Message, "gh token valid") {
		t.Fatalf("github-token check = %+v, want PASS from gh", check)
	}
}

func TestWarnsWhenGitHubTokenMissing(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	check := findCheck(t, Run(context.Background(), deps), "github-token")
	if check.Level != Warn || !strings.Contains(check.Message, "no GitHub token found") {
		t.Fatalf("github-token check = %+v, want WARN missing token", check)
	}
}

func TestFailsExpiredGitHubToken(t *testing.T) {
	setConfigEnv(t)
	srv := githubServer(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`, "")
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
	t.Setenv("GITHUB_TOKEN", "expired-token")
	deps.HTTPClient = srv.Client()
	deps.GitHubRESTBase = srv.URL

	check := findCheck(t, Run(context.Background(), deps), "github-token")
	if check.Level != Fail || !strings.Contains(check.Message, "HTTP 401") {
		t.Fatalf("github-token check = %+v, want FAIL rejected token", check)
	}
}

// TestChecksAOBinaryIdentity covers the `ao-binary` check: workspace hooks
// invoke a bare `ao hooks <agent> <event>`, so doctor must surface when the
// `ao` on PATH is not the running binary (e.g. a legacy CLI without the hooks
// command shadowing the Go one).
func TestChecksAOBinaryIdentity(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "ao")
	other := filepath.Join(dir, "ao-legacy")
	for _, p := range []string{self, other} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture must be executable-shaped
			t.Fatal(err)
		}
	}
	selfExe := func() (string, error) { return self, nil }

	cases := []struct {
		name       string
		executable func() (string, error)
		paths      map[string]string
		wantLevel  Level
		wantIn     string
	}{
		{"ao in PATH is this binary", selfExe, map[string]string{"ao": self}, Pass, "this binary"},
		{"ao in PATH is a different binary", selfExe, map[string]string{"ao": other}, Warn, "not this binary"},
		{"ao missing from PATH", selfExe, map[string]string{}, Warn, "not found in PATH"},
		{"running executable unresolvable", func() (string, error) { return "", errors.New("no exe") }, map[string]string{"ao": self}, Warn, "could not resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := Deps{Executable: tc.executable, LookPath: lookPathIn(tc.paths)}.withDefaults()
			check := deps.checkAOBinary()
			if check.Level != tc.wantLevel || !strings.Contains(check.Message, tc.wantIn) {
				t.Fatalf("ao-binary check = %+v, want level %s with %q", check, tc.wantLevel, tc.wantIn)
			}
		})
	}
}

// TestIncludesAOBinaryCheck asserts Run actually surfaces the ao-binary check,
// so the identity probe cannot silently fall out of the report.
func TestIncludesAOBinaryCheck(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	// testDeps' LookPath has no "ao", so the check lands as a WARN.
	check := findCheck(t, Run(context.Background(), deps), "ao-binary")
	if check.Level != Warn || !strings.Contains(check.Message, "not found in PATH") {
		t.Fatalf("ao-binary check = %+v, want WARN for missing ao", check)
	}
}

func codexCanaryFake(t *testing.T, probeOutput string, probeErr error) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case name == "/bin/codex" && len(args) == 1 && args[0] == "--version":
			return []byte("codex-cli 0.136.0\n"), nil
		case name == "/bin/codex":
			return []byte(probeOutput), probeErr
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	}
}

func TestCodexLaunchFlagsPass(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"}, codexCanaryFake(t, "ok\n", nil))

	check := findCheck(t, Run(context.Background(), deps), "codex-launch-flags")
	if check.Level != Pass || !strings.Contains(check.Message, "accepts") {
		t.Fatalf("canary = %+v, want PASS accepts", check)
	}
}

func TestCodexLaunchFlagsWarnOnRejectedFlag(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"},
		codexCanaryFake(t, "error: unexpected argument '--dangerously-bypass-hook-trust' found\n", errors.New("exit status 2")))

	check := findCheck(t, Run(context.Background(), deps), "codex-launch-flags")
	if check.Level != Warn || !strings.Contains(check.Message, "rejected AO's launch flags") {
		t.Fatalf("canary = %+v, want WARN rejected flags", check)
	}
}

func TestCodexLaunchFlagsWarnOnUnknownConfigField(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"},
		codexCanaryFake(t, "unknown configuration field `hooks` in -c/--config override\n", nil))

	check := findCheck(t, Run(context.Background(), deps), "codex-launch-flags")
	if check.Level != Warn || !strings.Contains(check.Message, "no longer recognizes") {
		t.Fatalf("canary = %+v, want WARN unknown config field", check)
	}
}

func TestCodexLaunchFlagsSkippedWithoutCodex(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)

	check := findCheck(t, Run(context.Background(), deps), "codex-launch-flags")
	if check.Level != Pass || !strings.Contains(check.Message, "skipped") {
		t.Fatalf("canary = %+v, want skipped PASS", check)
	}
}

func TestHooksLogStates(t *testing.T) {
	t.Run("missing log passes", func(t *testing.T) {
		setConfigEnv(t)
		deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findCheck(t, Run(context.Background(), deps), "hooks-log")
		if check.Level != Pass || !strings.Contains(check.Message, "no hook delivery failures") {
			t.Fatalf("hooks-log = %+v, want PASS no failures", check)
		}
	})

	t.Run("recent failures warn", func(t *testing.T) {
		dataDir := setConfigEnv(t)
		writeHooksLogLines(t, dataDir,
			time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339)+" session=old ao hooks codex stop: stale",
			time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)+" session=mer-1 ao hooks codex stop: connection refused",
		)
		deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findCheck(t, Run(context.Background(), deps), "hooks-log")
		if check.Level != Warn || !strings.Contains(check.Message, "1 hook delivery failure") || !strings.Contains(check.Message, "connection refused") {
			t.Fatalf("hooks-log = %+v, want WARN with recent count and latest line", check)
		}
	})

	t.Run("only stale failures pass", func(t *testing.T) {
		dataDir := setConfigEnv(t)
		writeHooksLogLines(t, dataDir,
			time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339)+" session=old ao hooks codex stop: stale",
		)
		deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findCheck(t, Run(context.Background(), deps), "hooks-log")
		if check.Level != Pass || !strings.Contains(check.Message, "last 24h") {
			t.Fatalf("hooks-log = %+v, want PASS stale-only", check)
		}
	})
}

// TestDaemonCheckIsInjected pins the one point the two callers differ on: the
// CLI supplies its run-file inspection, the daemon answers for itself, and a
// caller that supplies neither gets no daemon check rather than a wrong one.
func TestDaemonCheckIsInjected(t *testing.T) {
	setConfigEnv(t)
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
	for _, check := range Run(context.Background(), deps) {
		if check.Name == "daemon" {
			t.Fatalf("daemon check reported with no DaemonCheck injected: %+v", check)
		}
	}

	deps.DaemonCheck = func(context.Context) Check {
		return Check{Level: Pass, Section: SectionCore, Name: "daemon", Message: "injected"}
	}
	check := findCheck(t, Run(context.Background(), deps), "daemon")
	if check.Message != "injected" {
		t.Fatalf("daemon check = %+v, want the injected one", check)
	}
}

func gitOnly(context.Context, string, ...string) ([]byte, error) {
	return []byte("git version 2.43.0\n"), nil
}

// setConfigEnv points config.Load at a temp data dir and returns it, so the
// filesystem checks never touch the developer's real ~/.ao.
func setConfigEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	t.Setenv("AO_RUN_FILE", filepath.Join(dir, "running.json"))
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Setenv("AO_PORT", "3001")
	t.Setenv("AO_REQUEST_TIMEOUT", "")
	t.Setenv("AO_SHUTDOWN_TIMEOUT", "")
	return dataDir
}

func lookPathIn(paths map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		path, ok := paths[name]
		if !ok || path == "" {
			return "", fmt.Errorf("%s missing", name)
		}
		return path, nil
	}
}

func testDeps(t *testing.T, paths map[string]string, commandOutput func(context.Context, string, ...string) ([]byte, error)) Deps {
	t.Helper()
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	deps := Deps{LookPath: lookPathIn(paths), CommandOutput: commandOutput}
	return deps.withDefaults()
}

func githubServer(t *testing.T, status int, body, scopes string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Fatalf("unexpected github probe: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("missing bearer auth header: %q", got)
		}
		if scopes != "" {
			w.Header().Set("X-OAuth-Scopes", scopes)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor check %q not found in %+v", name, checks)
	return Check{}
}

func writeHooksLogLines(t *testing.T, dataDir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, HooksLogName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeAuthProbeGetsItsOwnBudget is the readiness signal not lying. The
// generic probeTimeout is sized for `git --version`; `claude auth status
// --json` is a Node CLI cold start, which on a 1 vCPU VM routinely takes
// longer and may touch the network. Timing out there produces a WARN the
// desktop maps to "harness missing", so a correctly signed-in machine would
// report as unconfigured and send the user back to setup.
func TestClaudeAuthProbeGetsItsOwnBudget(t *testing.T) {
	setConfigEnv(t)
	budgets := map[string]time.Duration{}
	deps := testDeps(t, map[string]string{"git": "/bin/git", "claude": "/bin/claude"},
		func(ctx context.Context, name string, args ...string) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("probe %s %v ran with no deadline at all", name, args)
			}
			budgets[name+" "+strings.Join(args, " ")] = time.Until(deadline)
			switch {
			case name == "/bin/git":
				return []byte("git version 2.43.0\n"), nil
			case strings.Join(args, " ") == "--version":
				return []byte("2.1.220 (Claude Code)\n"), nil
			default:
				return []byte(`{"loggedIn":true,"authMethod":"claudeai"}`), nil
			}
		})

	if check := findCheck(t, Run(context.Background(), deps), "claude-auth"); check.Level != Pass {
		t.Fatalf("claude-auth check = %+v, want PASS", check)
	}

	authBudget := budgets["/bin/claude auth status --json"]
	if authBudget <= probeTimeout {
		t.Errorf("claude auth probe budget = %s, want more than the generic probe timeout %s", authBudget, probeTimeout)
	}
	if authBudget > harnessAuthTimeout {
		t.Errorf("claude auth probe budget = %s, want at most %s", authBudget, harnessAuthTimeout)
	}
	// The cheap local probes keep the small budget: a slow harness is no
	// reason to let `git --version` hang the report.
	if gitBudget := budgets["/bin/git --version"]; gitBudget > probeTimeout {
		t.Errorf("git probe budget = %s, want the generic probe timeout %s", gitBudget, probeTimeout)
	}
}

// TestParseClaudeAuthStatusTruncatesEchoedOutput: this error becomes the check
// Message, which GET /api/v1/doctor serves, so whatever a future claude release
// prints on a zero-exit non-JSON path must not be echoed wholesale.
func TestParseClaudeAuthStatusTruncatesEchoedOutput(t *testing.T) {
	noise := strings.Repeat("x", 5000)
	_, err := parseClaudeAuthStatus([]byte(noise))
	if err == nil {
		t.Fatal("expected an error: there is no JSON object in the output")
	}
	if len(err.Error()) > maxProbeOutputInMessage+100 {
		t.Fatalf("error is %d bytes, want the echoed output truncated near %d", len(err.Error()), maxProbeOutputInMessage)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error = %q, want it to say the output was truncated", err.Error())
	}
}

// TestGitHubTokenPassKeepsIdentityLocal: `ao doctor` on the machine names the
// account and the token's scopes, because that is the useful local answer. The
// HTTP projection gets PublicMessage, which answers only "is the token good":
// the login is the GitHub identity this machine acts as, and the scope list is
// the exact capability of the credential sitting on it.
func TestGitHubTokenPassKeepsIdentityLocal(t *testing.T) {
	setConfigEnv(t)
	srv := githubServer(t, http.StatusOK, `{"login":"octocat"}`, "repo, workflow, read:org")
	deps := testDeps(t, map[string]string{"git": "/bin/git"}, gitOnly)
	t.Setenv("AO_GITHUB_TOKEN", "env-token")
	deps.HTTPClient = srv.Client()
	deps.GitHubRESTBase = srv.URL

	check := findCheck(t, Run(context.Background(), deps), "github-token")
	if check.Level != Pass {
		t.Fatalf("github-token check = %+v, want PASS", check)
	}
	if !strings.Contains(check.Message, "octocat") || !strings.Contains(check.Message, "repo, workflow, read:org") {
		t.Errorf("local message = %q, want the login and scopes kept for `ao doctor`", check.Message)
	}
	if check.PublicMessage == "" {
		t.Fatal("public message is empty, so the HTTP route would serve the login and scopes")
	}
	if strings.Contains(check.PublicMessage, "octocat") || strings.Contains(check.PublicMessage, "read:org") {
		t.Errorf("public message = %q, want neither the login nor the scope list", check.PublicMessage)
	}
	if !strings.Contains(check.PublicMessage, "AO_GITHUB_TOKEN") {
		t.Errorf("public message = %q, want it to still name which credential answered", check.PublicMessage)
	}
}
