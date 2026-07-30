package cli

import (
	"fmt"
	"log/slog"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// newVMCommand groups commands that run on a hosted VM gateway machine.
func newVMCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Commands for a hosted VM gateway machine",
	}
	cmd.AddCommand(newVMServeCommand(ctx))
	cmd.AddCommand(newVMSetupHarnessCommand(ctx))
	return cmd
}

func newVMServeCommand(ctx *commandContext) *cobra.Command {
	var opts vmgateway.Options
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the public TLS gateway that fronts the loopback daemon on a hosted VM",
		Long: "ao vm serve binds :80 and :443, obtains and renews a Let's Encrypt\n" +
			"certificate for the configured domain via ACME, verifies the AO access\n" +
			"token on every request, and reverse-proxies authenticated requests to the\n" +
			"loopback daemon. It never proxies loopback-only control routes.\n\n" +
			"It runs as its own process, separate from the daemon, normally started by\n" +
			"systemd on a machine provisioned by `ao setup-vm` (see\n" +
			"docs/adr/0002-hosted-public-gateway.md). Configuration is read from\n" +
			"~/.ao/machine.json when present; every value can also be set with a flag\n" +
			"or environment variable, which take precedence over machine.json.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runVMServe(cmd, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Domain, "domain", "", "Public domain this gateway serves (default: machine.json)")
	flags.StringVar(&opts.MachineID, "machine-id", "", "This machine's id, checked against the token's aud (default: machine.json)")
	flags.StringVar(&opts.AccountID, "account-id", "", "The single allowlisted account id, checked against the token's sub (default: machine.json)")
	flags.StringVar(&opts.Issuer, "issuer", "", fmt.Sprintf("Expected token issuer (default %s)", vmgateway.DefaultIssuer))
	flags.StringVar(&opts.JWKSURL, "jwks-url", "", "Control-plane JWKS URL (default <issuer>/.well-known/jwks.json)")
	flags.StringVar(&opts.DaemonAddr, "daemon-addr", "", "Loopback daemon host:port (default: discovered from running.json)")
	flags.StringVar(&opts.MachineFile, "machine-file", "", "machine.json path (default ~/.ao/machine.json)")
	flags.StringVar(&opts.CertDir, "cert-dir", "", "ACME certificate cache directory (default under the AO data dir)")
	flags.StringVar(&opts.HTTPAddr, "http-addr", "", fmt.Sprintf("ACME HTTP-01 challenge / redirect listener address (default %s)", vmgateway.DefaultHTTPAddr))
	flags.StringVar(&opts.HTTPSAddr, "https-addr", "", fmt.Sprintf("Public TLS listener address (default %s)", vmgateway.DefaultHTTPSAddr))
	return cmd
}

func (c *commandContext) runVMServe(cmd *cobra.Command, opts vmgateway.Options) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

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

	verify := vmgateway.VerifyOptions{
		Issuer:   gwCfg.Issuer,
		Audience: gwCfg.MachineID,
		Subject:  gwCfg.AccountID,
		Skew:     vmgateway.DefaultSkew,
	}
	jwks := vmgateway.NewJWKSCache(gwCfg.JWKSURL, nil)
	handler, err := vmgateway.NewHandler(gwCfg.DaemonAddr, jwks, verify, config.DefaultAllowedOrigins, log)
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
// systemd, and the reverse proxy will just return 502 until it is.
func discoverDaemonAddr(runFilePath string) (string, bool) {
	info, err := runfile.Read(runFilePath)
	if err != nil || info == nil || info.Port <= 0 {
		return "", false
	}
	return fmt.Sprintf("%s:%d", config.LoopbackHost, info.Port), true
}
