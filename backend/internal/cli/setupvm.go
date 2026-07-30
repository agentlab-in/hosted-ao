package cli

// setupvm.go is the thin, privileged half of `ao setup-vm`: the probes that
// look at the box and the commands that mutate it. Every decision it makes
// lives in setupvm_plan.go, which is pure and unit-tested on every OS in the
// CLI E2E matrix. Keep this file mechanical: probe, hand the observation to a
// pure function, execute what it says.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

const (
	// setupVMHTTPTimeout covers the public-IP lookup and the reachability
	// probe. Both talk to the internet from a fresh VM, so the CLI's default
	// 2s loopback budget is too tight.
	setupVMHTTPTimeout = 10 * time.Second
	setupVMUserAgent   = "ao-agent-orchestrator/setup-vm"
	// setupUnitSettle is how long to wait after starting a unit before asking
	// whether it is still running. Long enough for an immediate crash to show up
	// as anything other than active, short enough to add nothing noticeable to a
	// healthy run.
	setupUnitSettle = 2 * time.Second
	// defaultSetupVMProbeURL is the off-box port prober. A cloud firewall is
	// invisible from inside the box, so confirming 80 and 443 needs a host that
	// is not this one, and the AO control plane is the one such host setup-vm
	// already depends on. Contract: GET ?host=<domain>&ports=80,443 answers
	// {"ports":{"80":true,"443":false}} after attempting a TCP connect from
	// outside. An unreachable prober is reported as unverified, never as a
	// closed port.
	defaultSetupVMProbeURL = vmgateway.DefaultIssuer + "/api/v1/reachability"
)

// setupVMPublicIPEndpoints are tried in order to learn this box's address as
// the internet sees it, which is what the DNS record has to match.
var setupVMPublicIPEndpoints = []string{"https://api.ipify.org", "https://ifconfig.me/ip"}

type setupVMOptions struct {
	Domain          string
	PublicIP        string
	ProbeURL        string
	ControlPlaneURL string
	DryRun          bool
}

func newSetupVMCommand(ctx *commandContext) *cobra.Command {
	var opts setupVMOptions
	cmd := &cobra.Command{
		Use:   "setup-vm",
		Short: "Preflight and install AO on a hosted Ubuntu VM",
		Long: "ao setup-vm prepares a hosted Ubuntu LTS VM to run remote agents. It gates\n" +
			"on the platform, preflights DNS, the public ports, and sudo before it touches\n" +
			"anything, then installs the ao binary, tmux, git, and gh, and writes two\n" +
			"systemd units: one for the loopback daemon and one for the public TLS gateway\n" +
			"(see docs/adr/0002-hosted-public-gateway.md).\n\n" +
			"It then binds this machine to an AO account over an RFC 8628 device code: it\n" +
			"prints a short code and a URL, waits for you to approve the machine in a browser\n" +
			"on any device, writes ~/.ao/machine.json, and restarts the gateway so it reads\n" +
			"the new binding.\n\n" +
			"A failed preflight changes nothing at all: it prints exactly what to fix and\n" +
			"exits. A successful run is idempotent, so running it again is safe; an\n" +
			"already-bound machine has its current binding printed and is then bound again.\n" +
			"The run ends by listing what is still missing with the exact command for each.\n\n" +
			"It does not install an agent harness and does not configure git credentials,\n" +
			"because both need an interactive login.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runSetupVM(cmd, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Domain, "domain", "", "Public hostname you own, with a DNS record pointing at this machine (required)")
	flags.StringVar(&opts.PublicIP, "public-ip", "", "This machine's public IP, for when it cannot be discovered automatically")
	flags.StringVar(&opts.ProbeURL, "reachability-probe-url", defaultSetupVMProbeURL, "Off-box prober used to confirm 80 and 443 are reachable from the internet")
	flags.StringVar(&opts.ControlPlaneURL, "control-plane-url", vmgateway.DefaultIssuer, "AO control plane this machine is bound against")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "Gate, preflight, print the plan and both unit files, and change nothing")
	return cmd
}

func (c *commandContext) runSetupVM(cmd *cobra.Command, opts setupVMOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	domain, err := normalizeSetupDomain(opts.Domain)
	if err != nil {
		return usageError{err}
	}
	controlPlaneURL := strings.TrimRight(strings.TrimSpace(opts.ControlPlaneURL), "/")
	if controlPlaneURL == "" {
		return usageError{errors.New("--control-plane-url is required, for example --control-plane-url " + vmgateway.DefaultIssuer)}
	}

	platform := c.probeSetupPlatform()
	warnings, err := checkSetupPlatform(platform)
	if err != nil {
		if writeErr := writeSetupText(out, renderManualPath(platform, domain)); writeErr != nil {
			return writeErr
		}
		return err
	}

	preflight := c.probeSetupPreflight(ctx, domain, opts)
	problems, preflightWarnings := evaluatePreflight(preflight)
	warnings = append(warnings, preflightWarnings...)
	if len(problems) > 0 {
		if writeErr := writeSetupText(out, renderPreflightFailure(problems)); writeErr != nil {
			return writeErr
		}
		return errors.New("ao setup-vm preflight failed, so nothing on this machine was changed")
	}

	plan, err := c.buildSetupVMPlan(domain)
	if err != nil {
		return err
	}

	if opts.DryRun {
		return writeSetupText(out, renderSetupDryRun(plan, warnings))
	}

	units, notes, err := c.installSetupVM(ctx, out, plan)
	if err != nil {
		return err
	}

	bindErr := c.bindSetupVM(ctx, out, plan, controlPlaneURL)
	if bindErr == nil {
		plan.Bound = true
		// The binding step restarted the gateway, so whether it is running is a
		// fresh question: it reads machine.json at startup and refuses to serve
		// on anything it cannot parse.
		gatewayActive, gatewayNote := c.confirmSetupUnitActive(ctx, setupVMGatewayUnit)
		units.GatewayRunning = gatewayActive
		if gatewayNote != "" {
			notes = append(notes, gatewayNote)
		}
	} else {
		// The install is done and correct; only the binding is missing. The
		// summary still has to print, because it is what tells the user how to
		// finish, and it is now the only place that says so.
		notes = append(notes, "binding did not complete: "+bindErr.Error())
	}

	if err := writeSetupText(out, renderSetupSummary(plan, units, append(warnings, notes...))); err != nil {
		return err
	}
	return bindErr
}

// ---------------------------------------------------------------------------
// probes
// ---------------------------------------------------------------------------

func (c *commandContext) probeSetupPlatform() setupPlatform {
	platform := setupPlatform{GOOS: runtime.GOOS, OSRelease: map[string]string{}}
	if runtime.GOOS != "linux" {
		return platform
	}
	if content, err := os.ReadFile("/etc/os-release"); err == nil {
		platform.OSRelease = parseOSRelease(string(content))
	}
	_, systemctlErr := c.deps.LookPath("systemctl")
	_, aptErr := c.deps.LookPath("apt-get")
	platform.HasSystemctl = systemctlErr == nil
	platform.HasAptGet = aptErr == nil
	return platform
}

func (c *commandContext) probeSetupPreflight(ctx context.Context, domain string, opts setupVMOptions) setupPreflight {
	preflight := setupPreflight{Domain: domain, UID: os.Getuid()}

	// Who the units would run as is a preflight fact, not an install-time one: a
	// root target has to be refused before anything on this box is touched. A
	// lookup failure is left empty here and reported by buildSetupPlan, which is
	// where the error already has the better wording for it.
	if target, err := setupTargetUser(); err == nil {
		preflight.TargetUser = target.Username
	}

	if sudoPath, err := c.deps.LookPath("sudo"); err == nil {
		preflight.SudoPath = sudoPath
		_, sudoErr := c.deps.CommandOutput(ctx, "sudo", "-n", "true")
		preflight.SudoPasswordless = sudoErr == nil
	}

	// The DMI vendor only selects which cloud's firewall instructions to print,
	// so an unreadable file is not worth reporting.
	if vendor, err := os.ReadFile("/sys/class/dmi/id/sys_vendor"); err == nil {
		preflight.Cloud = setupCloudFromVendor(string(vendor))
	}

	preflight.PublicIP = strings.TrimSpace(opts.PublicIP)
	switch {
	case preflight.PublicIP == "":
		preflight.PublicIP, preflight.PublicIPErr = c.discoverPublicIP(ctx)
	case net.ParseIP(preflight.PublicIP) == nil:
		preflight.PublicIPErr = fmt.Errorf("--public-ip %q is not an IP address", preflight.PublicIP)
	}

	preflight.ResolvedIPs, preflight.ResolveErr = net.DefaultResolver.LookupHost(ctx, domain)

	gatewayActive := c.setupUnitActive(ctx, setupVMGatewayUnit)
	preflight.GatewayActive = gatewayActive
	preflight.Ports = probeSetupPorts(setupVMPorts, gatewayActive)
	preflight.Reach = c.probeSetupReachability(ctx, opts.ProbeURL, domain, gatewayActive)
	return preflight
}

// probeSetupPorts checks that :80 and :443 can be bound, by binding them on
// the loopback interface and releasing them immediately. Loopback is enough to
// catch both real failures: privilege (ports under 1024 need it, on loopback
// too) and a conflicting listener such as a distro nginx on 0.0.0.0:80. It is
// deliberately not a public bind: AGENTS.md permits exactly one of those,
// `ao vm serve`, and a preflight check is not it.
func probeSetupPorts(ports []int, gatewayActive bool) []setupPortProbe {
	probes := make([]setupPortProbe, 0, len(ports))
	for _, port := range ports {
		probe := setupPortProbe{Port: port}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			if closeErr := listener.Close(); closeErr != nil {
				probe.Err = closeErr
			}
		} else {
			probe.Err = err
			probe.HeldByGateway = gatewayActive && isSetupAddrInUse(err)
		}
		probes = append(probes, probe)
	}
	return probes
}

// isSetupAddrInUse classifies a bind failure by message rather than errno so
// this stays one code path on every OS the CLI is built for.
func isSetupAddrInUse(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "in use") || strings.Contains(msg, "address already")
}

func (c *commandContext) setupUnitActive(ctx context.Context, unit string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := c.deps.CommandOutput(ctx, "systemctl", "is-active", "--quiet", unit)
	return err == nil
}

// confirmSetupUnitActive answers whether a unit is really running, and returns
// the note to carry when it is not. `systemctl start` returning 0 is not
// evidence for these two units: both are Type=simple, so systemd considers the
// job done the instant the process is forked, even when it exits a millisecond
// later. The settle wait is what makes an immediate exit visible at all, since a
// crash-looping unit reports activating rather than active while it waits out
// RestartSec.
func (c *commandContext) confirmSetupUnitActive(ctx context.Context, unit string) (bool, string) {
	c.deps.Sleep(setupUnitSettle)
	if c.setupUnitActive(ctx, unit) {
		return true, ""
	}
	return false, fmt.Sprintf(
		"%s was started but is not active, so it exited on its own. This machine will not work"+
			"\n  until it stays up. Look at why:"+
			"\n    sudo systemctl status %s"+
			"\n    sudo journalctl -u %s -n 50",
		unit, unit, unit)
}

func (c *commandContext) setupHTTPClient() *http.Client {
	client := *c.deps.HTTPClient
	client.Timeout = setupVMHTTPTimeout
	return &client
}

// discoverPublicIP asks each endpoint in turn what this box's address looks like
// from the internet, and prefers an IPv4 answer. On a dual-stack box the answer
// is whichever family the request happened to go out over, and a v6 answer read
// against a perfectly good A record is reported as a DNS mismatch that does not
// exist. A v6-only box finds no v4 answer at any endpoint and keeps the one it
// has, so nothing is lost by looking.
func (c *commandContext) discoverPublicIP(ctx context.Context) (string, error) {
	client := c.setupHTTPClient()
	var lastErr error
	var otherFamily string
	for _, endpoint := range setupVMPublicIPEndpoints {
		ip, err := fetchSetupPublicIP(ctx, client, endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
			return ip, nil
		}
		if otherFamily == "" {
			otherFamily = ip
		}
	}
	if otherFamily != "" {
		return otherFamily, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no public IP endpoint configured")
	}
	return "", lastErr
}

func fetchSetupPublicIP(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	body, err := setupHTTPGet(ctx, client, endpoint, 256)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(body))
	if net.ParseIP(raw) == nil {
		return "", fmt.Errorf("%s did not answer with an IP address", endpoint)
	}
	return raw, nil
}

// probeSetupReachability asks an off-box prober whether 80 and 443 accept
// connections. It only runs when this machine's own gateway is already
// listening: with nothing bound, a closed answer would mean "no listener", not
// "blocked firewall", and reporting that as a firewall problem would be a lie.
// On a first run the check therefore reports unverified, and preflight prints
// the firewall instructions and the off-box command to confirm by hand.
func (c *commandContext) probeSetupReachability(ctx context.Context, probeURL, domain string, gatewayActive bool) setupReachability {
	if !gatewayActive {
		// evaluatePreflight words this case from GatewayActive: no error here,
		// because nothing failed.
		return setupReachability{}
	}
	probeURL = strings.TrimSpace(probeURL)
	if probeURL == "" {
		return setupReachability{Err: errors.New("no prober configured (--reachability-probe-url is empty)")}
	}
	parsed, err := url.Parse(probeURL)
	if err != nil {
		return setupReachability{Err: fmt.Errorf("invalid --reachability-probe-url %q: %w", probeURL, err)}
	}
	query := parsed.Query()
	query.Set("host", domain)
	query.Set("ports", joinSetupPorts(setupVMPorts))
	parsed.RawQuery = query.Encode()

	body, err := setupHTTPGet(ctx, c.setupHTTPClient(), parsed.String(), 4096)
	if err != nil {
		return setupReachability{Err: err}
	}
	open, err := parseSetupReachability(body, setupVMPorts)
	if err != nil {
		return setupReachability{Err: err}
	}
	return setupReachability{Ran: true, Open: open}
}

func setupHTTPGet(ctx context.Context, client *http.Client, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", setupVMUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", endpoint, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func joinSetupPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------
// the plan
// ---------------------------------------------------------------------------

// buildSetupVMPlan resolves the user the units run as and every absolute path
// they need. Under sudo the invoking process is root but the machine belongs to
// the human in SUDO_USER, and that is who must own ~/.ao and run the agents.
func (c *commandContext) buildSetupVMPlan(domain string) (setupPlan, error) {
	target, err := setupTargetUser()
	if err != nil {
		return setupPlan{}, err
	}
	in := setupPlanInput{
		Domain:      domain,
		User:        target.Username,
		Home:        target.HomeDir,
		DataDir:     os.Getenv("AO_DATA_DIR"),
		RunFile:     os.Getenv("AO_RUN_FILE"),
		MachineFile: os.Getenv("AO_MACHINE_FILE"),
	}
	if group, err := user.LookupGroupId(target.Gid); err == nil {
		in.Group = group.Name
	}
	plan, err := buildSetupPlan(in)
	if err != nil {
		return setupPlan{}, err
	}
	// Bound describes the machine as the install finds it, before this run
	// binds anything: it is what decides whether the gateway is started during
	// install, and whether the binding step prints a previous binding it is
	// about to replace.
	if _, err := os.Stat(plan.MachineFile); err == nil {
		plan.Bound = true
	}
	return plan, nil
}

// setupTargetUser is the unprivileged owner of the machine: SUDO_USER when the
// command was run through sudo, otherwise whoever is running it.
func setupTargetUser() (*user.User, error) {
	if name := strings.TrimSpace(os.Getenv("SUDO_USER")); name != "" && name != "root" {
		target, err := user.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("look up SUDO_USER %q: %w", name, err)
		}
		return target, nil
	}
	target, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("look up the current user: %w", err)
	}
	return target, nil
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

// installSetupVM performs every mutation, in order. It reports which units ended
// up actually running, plus any note the summary has to carry. Each step is
// skipped when it is already done, so a second run of ao setup-vm changes
// nothing and restarts nothing.
func (c *commandContext) installSetupVM(ctx context.Context, out io.Writer, plan setupPlan) (setupUnitStates, []string, error) {
	step := func(format string, args ...any) error {
		_, err := fmt.Fprintf(out, "==> "+format+"\n", args...)
		return err
	}
	var units setupUnitStates
	var notes []string

	for _, dir := range plan.setupDirs() {
		if err := c.runSetupPrivileged(ctx, "install", "-d", "-m", "0700", "-o", plan.User, "-g", plan.Group, dir); err != nil {
			return units, notes, err
		}
	}
	if err := step("state directories ready under %s, owned by %s", plan.AODir, plan.User); err != nil {
		return units, notes, err
	}

	if err := c.ensureSetupPackages(ctx, out, plan.Packages); err != nil {
		return units, notes, err
	}
	binaryChanged, err := c.ensureSetupBinary(ctx, out, plan)
	if err != nil {
		return units, notes, err
	}

	daemonChanged, err := c.writeSetupUnit(ctx, out, setupVMDaemonUnit, renderDaemonUnit(plan))
	if err != nil {
		return units, notes, err
	}
	gatewayChanged, err := c.writeSetupUnit(ctx, out, setupVMGatewayUnit, renderGatewayUnit(plan))
	if err != nil {
		return units, notes, err
	}
	if daemonChanged || gatewayChanged {
		if err := c.runSetupPrivileged(ctx, "systemctl", "daemon-reload"); err != nil {
			return units, notes, err
		}
	}

	if err := c.runSetupPrivileged(ctx, "systemctl", "enable", setupVMDaemonUnit, setupVMGatewayUnit); err != nil {
		return units, notes, err
	}
	// Only a changed unit earns a restart: restarting the daemon kills the
	// agent sessions it is supervising, which a re-run must not do. A new
	// binary is reported instead of acted on, for the same reason.
	daemonAction := "start"
	if daemonChanged {
		daemonAction = "restart"
	}
	if err := c.runSetupPrivileged(ctx, "systemctl", daemonAction, setupVMDaemonUnit); err != nil {
		return units, notes, err
	}
	daemonActive, daemonNote := c.confirmSetupUnitActive(ctx, setupVMDaemonUnit)
	units.DaemonRunning = daemonActive
	if daemonNote != "" {
		notes = append(notes, daemonNote)
	}
	if err := step("%s %s", setupVMDaemonUnit, setupUnitStateText(daemonActive)); err != nil {
		return units, notes, err
	}
	if binaryChanged && daemonAction == "start" {
		notes = append(notes, fmt.Sprintf(
			"the ao binary was replaced, but %s was left running on the previous build so live agent"+
				"\n  sessions were not killed. Restart it when nothing is mid-task: sudo systemctl restart %s",
			setupVMDaemonUnit, setupVMDaemonUnit))
	}

	if !plan.Bound {
		// `ao vm serve` reads machine.json once at startup and cannot serve
		// without it, so starting it now would only produce a restart loop.
		return units, notes, step("%s enabled but not started: this machine is not bound yet", setupVMGatewayUnit)
	}
	// The gateway holds no session state, so a new binary or unit is safe to
	// restart into immediately.
	gatewayAction := "start"
	if gatewayChanged || binaryChanged {
		gatewayAction = "restart"
	}
	if err := c.runSetupPrivileged(ctx, "systemctl", gatewayAction, setupVMGatewayUnit); err != nil {
		return units, notes, err
	}
	gatewayActive, gatewayNote := c.confirmSetupUnitActive(ctx, setupVMGatewayUnit)
	units.GatewayRunning = gatewayActive
	if gatewayNote != "" {
		notes = append(notes, gatewayNote)
	}
	return units, notes, step("%s %s", setupVMGatewayUnit, setupUnitStateText(gatewayActive))
}

// setupUnitStateText is what the progress line says about a unit that was just
// started. "running" is a claim, so it is only made when is-active agreed.
func setupUnitStateText(active bool) string {
	if active {
		return "enabled and running"
	}
	return "enabled, but not running: see the warnings at the end of this run"
}

func (c *commandContext) ensureSetupPackages(ctx context.Context, out io.Writer, packages []string) error {
	missing := make([]string, 0, len(packages))
	for _, pkg := range packages {
		installed, err := c.deps.CommandOutput(ctx, "dpkg-query", "-W", "-f=${Status}", pkg)
		if err != nil || !dpkgInstalled(string(installed)) {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		_, err := fmt.Fprintf(out, "==> packages already present: %s\n", strings.Join(packages, ", "))
		return err
	}
	if slices.Contains(missing, "gh") {
		if err := c.ensureGitHubCLIRepo(ctx); err != nil {
			return err
		}
	}
	if err := c.runSetupPrivileged(ctx, "apt-get", "update"); err != nil {
		return err
	}
	args := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y"}, missing...)
	if err := c.runSetupPrivileged(ctx, "env", args...); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "==> installed: %s\n", strings.Join(missing, ", "))
	return err
}

// ensureGitHubCLIRepo adds GitHub's own apt repository, which is where a
// current gh comes from. Ubuntu LTS either has no gh package at all (22.04) or
// a years-old one, and gh is what picks up the user's git credentials.
func (c *commandContext) ensureGitHubCLIRepo(ctx context.Context) error {
	if _, err := os.Stat(setupVMKeyringPath); err != nil {
		keyring, err := setupHTTPGet(ctx, c.setupHTTPClient(), setupVMKeyringURL, 1<<20)
		if err != nil {
			return fmt.Errorf("download the GitHub CLI signing key: %w", err)
		}
		if len(keyring) < 100 {
			return fmt.Errorf("the GitHub CLI signing key at %s came back empty", setupVMKeyringURL)
		}
		if err := c.runSetupPrivileged(ctx, "install", "-d", "-m", "0755", filepath.ToSlash(filepath.Dir(setupVMKeyringPath))); err != nil {
			return err
		}
		if _, err := c.writeSetupFile(ctx, setupVMKeyringPath, "0644", keyring); err != nil {
			return err
		}
	}
	arch := "amd64"
	if raw, err := c.deps.CommandOutput(ctx, "dpkg", "--print-architecture"); err == nil {
		if detected := strings.TrimSpace(firstOutputLine(raw)); detected != "" {
			arch = detected
		}
	}
	_, err := c.writeSetupFile(ctx, setupVMSourceListPath, "0644", []byte(githubCLISourceList(arch)))
	return err
}

// ensureSetupBinary puts the running ao binary at an absolute, stable path,
// because a systemd unit must never resolve its ExecStart through a PATH. It
// reports whether the installed binary actually changed.
func (c *commandContext) ensureSetupBinary(ctx context.Context, out io.Writer, plan setupPlan) (bool, error) {
	self, err := c.deps.Executable()
	if err != nil {
		return false, fmt.Errorf("locate the running ao binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if filepath.ToSlash(self) == plan.BinaryPath || setupSameFile(self, plan.BinaryPath) {
		_, err := fmt.Fprintf(out, "==> ao binary already current at %s\n", plan.BinaryPath)
		return false, err
	}
	if err := c.runSetupPrivileged(ctx, "install", "-m", "0755", "-o", "root", "-g", "root", self, plan.BinaryPath); err != nil {
		return false, err
	}
	_, err = fmt.Fprintf(out, "==> installed the ao binary to %s\n", plan.BinaryPath)
	return true, err
}

func (c *commandContext) writeSetupUnit(ctx context.Context, out io.Writer, name, content string) (bool, error) {
	dest := slashPath(setupVMUnitDir, name)
	changed, err := c.writeSetupFile(ctx, dest, "0644", []byte(content))
	if err != nil {
		return false, err
	}
	verb := "unchanged"
	if changed {
		verb = "written"
	}
	if _, err := fmt.Fprintf(out, "==> %s %s\n", dest, verb); err != nil {
		return changed, err
	}
	return changed, nil
}

// writeSetupFile writes dest only when its content differs, which is what
// makes re-running safe: no duplicated units and no file that grows on every
// run. It reports whether anything changed.
func (c *commandContext) writeSetupFile(ctx context.Context, dest, mode string, content []byte) (bool, error) {
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	tmp, err := os.CreateTemp("", "ao-setup-vm-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := c.runSetupPrivileged(ctx, "install", "-m", mode, "-o", "root", "-g", "root", tmpPath, dest); err != nil {
		return false, err
	}
	return true, nil
}

// runSetupPrivileged is the only place ao setup-vm mutates the machine. It
// prefixes sudo -n when the process is not already root; preflight has already
// established that one of the two is true.
func (c *commandContext) runSetupPrivileged(ctx context.Context, name string, args ...string) error {
	if os.Getuid() != 0 {
		args = append([]string{"-n", name}, args...)
		name = "sudo"
	}
	out, err := c.deps.CommandOutput(ctx, name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
}

// setupSameFile reports whether two files have identical content, so an
// already-current binary is not reinstalled on every run.
func setupSameFile(a, b string) bool {
	sumA, err := setupFileSum(a)
	if err != nil {
		return false
	}
	sumB, err := setupFileSum(b)
	if err != nil {
		return false
	}
	return sumA == sumB
}

func setupFileSum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func writeSetupText(out io.Writer, text string) error {
	_, err := io.WriteString(out, text)
	return err
}
