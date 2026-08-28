package cli

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// newVMCommand groups commands that run on a directly paired machine.
func newVMCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Commands for a directly paired machine",
	}
	cmd.AddCommand(newVMServeCommand(ctx))
	cmd.AddCommand(newVMSetupHarnessCommand(ctx))
	cmd.AddCommand(newVMRotatePasscodeCommand(ctx))
	return cmd
}

func newVMServeCommand(ctx *commandContext) *cobra.Command {
	var opts vmgateway.Options
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the certificate-pinned TLS gateway for a directly paired machine",
		Long: "ao vm serve binds the HTTPS listener, verifies the box passcode on every\n" +
			"request, and reverse-proxies authenticated requests to the loopback daemon.\n" +
			"It never proxies loopback-only control routes.\n\n" +
			"It runs as its own process, separate from the daemon, normally started by\n" +
			"systemd on a machine provisioned by `ao pair`.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runVMServe(cmd, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.DaemonAddr, "daemon-addr", "", "Loopback daemon host:port (default: discovered from running.json)")
	flags.StringVar(&opts.CertDir, "cert-dir", "", "Persisted self-signed certificate directory (default under the state root)")
	flags.StringVar(&opts.HTTPSAddr, "https-addr", "", fmt.Sprintf("Public TLS listener address (default %s)", vmgateway.DefaultHTTPSAddr))
	flags.StringVar(&opts.PasscodeDir, "passcode-dir", "", "Passcode hash storage directory (default under the state root)")
	opts.Pair = true
	return cmd
}

func (c *commandContext) runVMServe(cmd *cobra.Command, opts vmgateway.Options) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// pinnedDaemonAddr is true when the operator supplied --daemon-addr or
	// AO_VM_DAEMON_ADDR explicitly, as opposed to letting it default from
	// running.json (below) or vmgateway.Resolve's own DefaultDaemonAddr
	// fallback. Only in the unpinned case does resolveDaemonAddr get wired up
	// below: re-resolving on a proxy failure must never silently override an
	// address the operator chose on purpose.
	pinnedDaemonAddr := opts.DaemonAddr != "" || os.Getenv("AO_VM_DAEMON_ADDR") != ""
	if opts.DaemonAddr == "" {
		if addr, ok := discoverDaemonAddr(cfg.RunFilePath); ok {
			opts.DaemonAddr = addr
		}
	}

	gwCfg, err := vmgateway.Resolve(opts, cfg.DataDir)
	if err != nil {
		// Every missing field here is fixable by passing a flag (or by
		// running ao setup-vm), the same "user needs to do something
		// differently" shape as a bad CLI argument.
		return usageError{err}
	}

	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))

	// resolveDaemonAddr lets the gateway recover on its own if the daemon was
	// not up yet at gateway boot (discoverDaemonAddr returned nothing, so
	// gwCfg.DaemonAddr is DefaultDaemonAddr) or later restarts onto a
	// different port: the proxy re-reads running.json after a connection
	// failure instead of proxying to a dead port until this process is
	// restarted. nil when the address was pinned, so a re-resolve never
	// overrides it.
	var resolveDaemonAddr func() (string, bool)
	if !pinnedDaemonAddr {
		resolveDaemonAddr = func() (string, bool) { return discoverDaemonAddr(cfg.RunFilePath) }
	}

	// The credential check is mode-specific and mutually exclusive, mirroring
	// gwCfg.Mode itself: hosted mode verifies a machine-audience JWT against
	// the control plane's JWKS, exactly as before pair mode existed; pair
	// mode loads the persisted passcode store instead and never touches JWKS
	// at all. A missing or corrupt passcode store is fatal here, before any
	// socket is bound, per docs/adr/0003-pair-mode-gateway.md.
	passcodes, loadErr := vmgateway.LoadPasscodeStore(gwCfg.PasscodeDir)
	if loadErr != nil {
		return loadErr
	}
	var handler http.Handler
	handler, err = vmgateway.NewPairHandler(gwCfg.DaemonAddr, resolveDaemonAddr, passcodes, cfg.AllowedOrigins, log)
	if err != nil {
		return fmt.Errorf("build gateway handler: %w", err)
	}
	srv, err := vmgateway.NewServer(gwCfg, handler, log)
	if err != nil {
		return err
	}

	ctxSig, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("ao vm serve starting",
		"domain", gwCfg.Domain, "machineId", gwCfg.MachineID, "daemonAddr", gwCfg.DaemonAddr)
	return srv.Run(ctxSig, cfg.ShutdownTimeout)
}

// newVMRotatePasscodeCommand deliberately rolls a pair-mode box's passcode,
// the counterpart to the accidental non-rotation ao setup-vm --pair
// guarantees on every re-run. Named for symmetry with the other `ao vm`
// subcommands (serve, setup-harness): a verb naming what it does to this
// machine, run on the box itself.
func newVMRotatePasscodeCommand(ctx *commandContext) *cobra.Command {
	var passcodeDir, certDir string
	cmd := &cobra.Command{
		Use:   "rotate-passcode",
		Short: "Roll this pair-mode box's passcode and drop every connected client",
		Long: "ao vm rotate-passcode generates a fresh pair-mode passcode, replacing the one\n" +
			"ao setup-vm --pair printed (or the last rotation), and restarts\n" +
			"ao-gateway.service so the change takes effect immediately. Every client still\n" +
			"connected with the old passcode is dropped and has to enter the new one; the\n" +
			"pinned certificate is unaffected, so there is no fingerprint to re-check.\n\n" +
			"The new pairing string is printed exactly once, here, and is never written to disk\n" +
			"in plaintext. Run this on the box itself, the same place ao setup-vm --pair ran.\n\n" +
			"This is the deliberate counterpart to ao setup-vm --pair's own guarantee: running\n" +
			"setup again never rotates the passcode on its own (see\n" +
			"docs/adr/0003-pair-mode-gateway.md). Use this command when you actually want to.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runVMRotatePasscode(cmd, passcodeDir, certDir)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&passcodeDir, "passcode-dir", "", "Pair-mode passcode hash storage directory (default under the state root)")
	flags.StringVar(&certDir, "cert-dir", "", "Pair-mode certificate directory (default under the state root)")
	return cmd
}

func (c *commandContext) runVMRotatePasscode(cmd *cobra.Command, passcodeDirFlag, certDirFlag string) error {
	dir, err := c.resolvePairPasscodeDir(passcodeDirFlag)
	if err != nil {
		return err
	}

	store, err := vmgateway.LoadPasscodeStore(dir)
	if err != nil {
		return fmt.Errorf("no pair-mode passcode to rotate at %s: %w", dir, err)
	}
	newPasscode, err := store.Rotate()
	if err != nil {
		return fmt.Errorf("rotate the passcode: %w", err)
	}
	if owner, ownerErr := setupTargetUser(); ownerErr == nil {
		if err := chownSetupTree(dir, owner); err != nil {
			return fmt.Errorf("rotated the passcode but could not hand %s to %s: %w", dir, owner.Username, err)
		}
	}

	if err := c.runSetupPrivileged(cmd.Context(), "systemctl", "restart", setupVMGatewayUnit); err != nil {
		return fmt.Errorf("rotated the passcode but could not restart %s so it takes effect: %w", setupVMGatewayUnit, err)
	}

	// The pairing string is a best-effort enrichment on top of a rotation
	// that has already fully succeeded (the passcode is rotated and the
	// gateway restarted above): a certificate this command cannot load, or
	// no address to build a string from, must never turn a successful
	// rotation into a reported failure, so both are swallowed here and the
	// output silently falls back to the plaintext-passcode-only line.
	// vmgateway.PairCertExists gates the load so a missing certificate
	// directory can never cause this command to mint a brand new
	// certificate: that would silently break the "pinned certificate is
	// unaffected" guarantee this command's own help text makes above.
	var pairingString string
	if certDir, certErr := c.resolvePairCertDir(certDirFlag); certErr == nil && vmgateway.PairCertExists(certDir) {
		if cert, certErr := vmgateway.LoadOrCreatePairCertificate(certDir); certErr == nil {
			pairingString, _, _ = c.buildPairingString(cmd.Context(), cert, newPasscode)
		}
	}
	return writeSetupText(cmd.OutOrStdout(), renderPasscodeRotated(newPasscode, pairingString))
}

// The claude harness is the only one ao vm setup-harness supports in v1. Its
// login and its readiness probe are two halves of the same `claude auth`
// surface, so they stay pinned to one another: the harness name and the status
// probe live in internal/doctor, which owns the claude-auth check, and this
// command reads the name from there. If a claude release moves one, doctor's
// claude-auth check and this command break together and visibly, rather than
// one of them silently reporting the wrong thing.
const claudeHarnessName = doctor.ClaudeHarnessName

var claudeLoginArgs = []string{"auth", "login"}

func newVMSetupHarnessCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "setup-harness " + claudeHarnessName,
		Short: "Log in to an agent harness on this machine, in the foreground",
		Long: "ao vm setup-harness runs the harness's own interactive login and hands the\n" +
			"terminal over to it. The harness prints a URL and then waits for a code to be\n" +
			"pasted back, so the exchange cannot be scripted or run in the background: run\n" +
			"this on a real terminal (an SSH session into the VM is fine).\n\n" +
			"Only the claude harness is supported. `ao doctor` then reports whether the\n" +
			"harness is signed in as its claude-auth check, which is what the desktop app\n" +
			"reads for a machine's harness readiness.\n\n" +
			"Git credentials are separate and are not wrapped by ao: run `gh auth login`\n" +
			"once, and the daemon picks the credential up on its own.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runVMSetupHarness(cmd, args[0])
		},
	}
}

func (c *commandContext) runVMSetupHarness(cmd *cobra.Command, harness string) error {
	if strings.ToLower(strings.TrimSpace(harness)) != claudeHarnessName {
		return usageError{fmt.Errorf(
			"unsupported harness %q: only %q is supported (other harnesses are out of scope for now)",
			harness, claudeHarnessName)}
	}
	path, err := c.deps.LookPath(claudeHarnessName)
	if err != nil || path == "" {
		return fmt.Errorf("claude not found in PATH: install the Claude Code CLI on this machine first (ao setup-vm deliberately does not, because the login is interactive), then re-run `ao vm setup-harness %s`", claudeHarnessName)
	}

	out := cmd.OutOrStdout()
	login := strings.Join(claudeLoginArgs, " ")
	if _, err := fmt.Fprintf(out, "Handing the terminal to `%s %s`.\n"+
		"It prints a URL and then waits for a code to be pasted back, so finish the\n"+
		"login here rather than trying to script it.\n\n", path, login); err != nil {
		return err
	}

	// Everything around it is ao's; the login itself is the harness's. This
	// hand-off stays deliberately thin, so there is nothing here for a claude
	// release to break behind our back.
	if err := c.deps.RunInteractive(cmd.Context(), path, claudeLoginArgs...); err != nil {
		return fmt.Errorf("`claude %s` did not complete: %w", login, err)
	}

	_, err = fmt.Fprint(out, "\nHarness login finished. Confirm it with `ao doctor` (the claude-auth check),\n"+
		"and if this machine still needs git credentials, run `gh auth login`.\n")
	return err
}

// discoverDaemonAddr reads the loopback daemon's own running.json handshake
// to find the port it actually bound, the same source the rest of the CLI
// uses (see client.go). A missing or unreadable run-file is not fatal here:
// the daemon may simply not be up yet when the gateway starts under
// systemd, and the reverse proxy will just return 502 until it is. Used both
// for the gateway's initial daemon address and, via resolveDaemonAddr in
// runVMServe, as the callback the gateway's proxy re-runs after a connection
// failure to pick up a daemon that started late or moved to a new port.
func discoverDaemonAddr(runFilePath string) (string, bool) {
	info, err := runfile.Read(runFilePath)
	if err != nil || info == nil || info.Port <= 0 {
		return "", false
	}
	return fmt.Sprintf("%s:%d", config.LoopbackHost, info.Port), true
}
