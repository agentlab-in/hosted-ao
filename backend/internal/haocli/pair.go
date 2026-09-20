package haocli

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// pairSchemaVersion versions the machine-readable pair reports. Bump when a
// field is removed or changes meaning; additive fields keep the version.
const pairSchemaVersion = 1

// pairShowReport is the versioned read-only identity report. It never carries
// the passcode plaintext or a pairing string: neither can be recovered from
// the surviving identity state, so show only proves the identity and points at
// rotate for recovery.
type pairShowReport struct {
	SchemaVersion   int      `json:"schemaVersion"`
	CertificatePath string   `json:"certificatePath"`
	Fingerprint     string   `json:"fingerprint"`
	Addresses       []string `json:"addresses"`
	PasscodePath    string   `json:"passcodePath"`
	PasscodeState   string   `json:"passcodeState"`
	PairingHint     string   `json:"pairingHint,omitempty"`
}

// pairRotateReport is the versioned outcome of one rotation. The pairingString
// field intentionally bypasses output redaction, exactly like init's: the
// rotation's whole point is that the minted pairing string is printed exactly
// once, and it is the only secret the report carries.
type pairRotateReport struct {
	SchemaVersion   int                `json:"schemaVersion"`
	CertificatePath string             `json:"certificatePath"`
	Fingerprint     string             `json:"fingerprint"`
	Addresses       []string           `json:"addresses,omitempty"`
	PasscodeState   string             `json:"passcodeState"`
	PairingString   string             `json:"pairingString,omitempty"`
	GatewayRestart  *pairRestartReport `json:"gatewayRestart,omitempty"`
	DryRun          bool               `json:"dryRun"`
}

// pairRestartReport describes what happened to the gateway service after the
// passcode hash changed: the restart that applied it, or the reason and manual
// commands when the host has no supported service manager (or the unit is not
// loaded, e.g. a freshly restored host before services are re-enabled).
type pairRestartReport struct {
	Supported bool                   `json:"supported"`
	Manager   string                 `json:"manager"`
	Reason    string                 `json:"reason,omitempty"`
	Manual    []string               `json:"manual,omitempty"`
	Result    ServiceOperationResult `json:"result,omitempty"`
	States    []ManagedServiceState  `json:"states,omitempty"`
}

// newPairCommand groups the pair-mode identity management commands. Pair-mode
// provisioning itself lives in `hao init --mode pair`, so the group is
// intentionally not a provisioning entry point: invoking it without a
// subcommand is a usage error, not a silent help screen.
func newPairCommand(deps Deps, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Show or rotate this machine's pair-mode identity",
		Long:  "Inspect or mutate the persistent pair-mode identity this machine uses to\naccept connections from the Hosted AO desktop app. The identity (certificate,\nprivate key, and the SHA-256 hash of the passcode) lives under the machine\nstate root and survives reinstalls of the desktop app or the host. Provisioning\nhappens through `hao init --mode pair`; this command only shows or rotates the\nsurviving identity.",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return commandError{Code: "invalid_usage", Message: "hao pair requires a subcommand", Operation: "pair management", Remediation: "run `hao pair show` to inspect the identity, or `hao pair rotate` to mint a fresh pairing string", Details: map[string]any{}, ExitStatus: 2}
		},
	}
	cmd.AddCommand(newPairShowCommand(deps, opts))
	cmd.AddCommand(newPairRotateCommand(deps, opts))
	return cmd
}

func newPairShowCommand(deps Deps, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show this machine's pair-mode identity without changing anything",
		Long:  "Prints the pair-mode certificate path, its fingerprint, and the advertised\npairing addresses without rotating or writing anything. The passcode plaintext\nis never stored, so its pairing string is not reprinted here: recovery mints a\nfresh one from this same identity with `hao pair rotate`.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := runPairShow(deps, opts)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writePairShow(cmd, report)
		},
	}
}

type pairRotateOptions struct {
	NonInteractive bool
	DryRun         bool
}

func newPairRotateCommand(deps Deps, opts *options) *cobra.Command {
	var io pairRotateOptions
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Rotate the pair-mode passcode and print a fresh pairing string",
		Long:  "Mints a fresh passcode for the existing pair-mode certificate, writes its\nSHA-256 hash into the passcode store, restarts the gateway service so the new\npasscode takes effect, and prints a fresh pairing string exactly once. The\ncertificate and its fingerprint never change, so paired desktop apps do not\nneed to re-verify a new fingerprint. Missing identity state is reported with\nprovisioning guidance before anything is mutated.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := runPairRotate(cmd.Context(), deps, opts, io)
			if err != nil {
				if report.PasscodeState == "rotated" {
					// Rotation already happened; emit the report first so the
					// pairing string is never lost to the failure envelope,
					// matching init's report-before-silent-failure contract.
					if opts.json {
						if writeErr := writeJSON(cmd.OutOrStdout(), report); writeErr != nil {
							return writeErr
						}
					} else if writeErr := writePairRotate(cmd, report); writeErr != nil {
						return writeErr
					}
				}
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writePairRotate(cmd, report)
		},
	}
	cmd.Flags().BoolVar(&io.NonInteractive, "non-interactive", false, "never prompt; privilege operations fail instead")
	cmd.Flags().BoolVar(&io.DryRun, "dry-run", false, "print the rotation plan without changing anything")
	return cmd
}

func runPairShow(deps Deps, opts *options) (pairShowReport, error) {
	report := pairShowReport{SchemaVersion: pairSchemaVersion}
	certDir, passcodeDir, err := resolvePairIdentityDirectories()
	if err != nil {
		return report, operationalError("resolve pair identity directories", err)
	}
	report.CertificatePath, report.PasscodePath = certDir, passcodeDir

	if !vmgateway.PairCertExists(certDir) {
		return report, pairNotProvisionedError("show pair identity", "certificate", certDir)
	}
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		return report, operationalError("load pair certificate", err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		return report, operationalError("render pair fingerprint", err)
	}
	report.Fingerprint = fingerprint

	if _, err := vmgateway.LoadPasscodeStore(passcodeDir); err != nil {
		return report, commandError{
			Code:        "pair_not_provisioned",
			Message:     fmt.Sprintf("pair mode: the passcode store at %s is missing or unreadable", passcodeDir),
			Operation:   "show pair identity",
			Remediation: "provision this machine first with `hao init --mode pair`",
			Details:     map[string]any{"passcodePath": passcodeDir, "diagnostic": safeDiagnostic(err)},
			ExitStatus:  1,
			Cause:       err,
		}
	}
	report.PasscodeState = "present"
	report.Addresses = machineAddresses(pairListenPort(deps, opts.configPath))
	report.PairingHint = "the pairing string is printed only when the passcode is minted or rotated, because its plaintext is never stored; mint a fresh one from this same identity with `hao pair rotate`"
	return report, nil
}

// runPairRotate rotates only the passcode: the certificate and its fingerprint
// are reused verbatim, and the command fails before any mutation when either
// half of the surviving identity is missing. The gateway restart is the last
// step so a failure can never destroy the freshly emitted pairing string; the
// post-rotation failure is reported after that string.
func runPairRotate(ctx context.Context, deps Deps, opts *options, io pairRotateOptions) (pairRotateReport, error) {
	report := pairRotateReport{SchemaVersion: pairSchemaVersion, DryRun: io.DryRun}
	certDir, passcodeDir, err := resolvePairIdentityDirectories()
	if err != nil {
		return report, operationalError("resolve pair identity directories", err)
	}
	report.CertificatePath = certDir

	if !vmgateway.PairCertExists(certDir) {
		return report, pairNotProvisionedError("rotate pair passcode", "certificate", certDir)
	}
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		return report, operationalError("load pair certificate", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return report, operationalError("parse pair certificate leaf", err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		return report, operationalError("render pair fingerprint", err)
	}
	report.Fingerprint = fingerprint

	store, err := vmgateway.LoadPasscodeStore(passcodeDir)
	if err != nil {
		return report, commandError{
			Code:        "pair_not_provisioned",
			Message:     fmt.Sprintf("pair mode: no passcode store to rotate at %s", passcodeDir),
			Operation:   "rotate pair passcode",
			Remediation: "provision this machine first with `hao init --mode pair`",
			Details:     map[string]any{"passcodePath": passcodeDir, "diagnostic": safeDiagnostic(err)},
			ExitStatus:  1,
			Cause:       err,
		}
	}
	report.Addresses = machineAddresses(pairListenPort(deps, opts.configPath))

	if io.DryRun {
		report.PasscodeState = "would-rotate"
		report.GatewayRestart = planPairGatewayRestart(ctx, deps, io.NonInteractive)
		return report, nil
	}

	plaintext, err := store.Rotate()
	if err != nil {
		return report, operationalError("rotate pair passcode", err)
	}
	report.PasscodeState = "rotated"

	pairingString, err := pairstring.Build(report.Addresses, pairstring.Fingerprint(leaf), plaintext)
	if err != nil {
		return report, operationalError("build pairing string", err)
	}
	report.PairingString = pairingString

	restart, restartErr := restartPairGateway(ctx, deps, io.NonInteractive)
	report.GatewayRestart = restart
	if restartErr != nil {
		return report, restartErr
	}
	return report, nil
}

// pairListenPort resolves the advertised pairing port: the explicit gateway
// override (AO_VM_HTTPS_ADDR, the same environment the setup unit render
// reads), then the configured pair.listenPort, then the canonical default. The
// hao configuration is consulted best-effort because the pair commands must
// work in recovery with no configuration at all.
func pairListenPort(deps Deps, explicit string) int {
	if addr := strings.TrimSpace(os.Getenv("AO_VM_HTTPS_ADDR")); addr != "" {
		if port := listenPortFromAddr(addr); port > 0 {
			return port
		}
	}
	if _, object, err := loadConfig(deps, explicit); err == nil {
		if port := configInt(object, "pair", "listenPort"); port > 0 {
			return port
		}
	}
	if port := listenPortFromAddr(vmgateway.DefaultHTTPSAddr); port > 0 {
		return port
	}
	return 443
}

func listenPortFromAddr(addr string) int {
	port, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(addr), ":"))
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func pairNotProvisionedError(operation, missing, path string) error {
	return commandError{
		Code:        "pair_not_provisioned",
		Message:     fmt.Sprintf("pair mode: no %s found at %s", missing, path),
		Operation:   operation,
		Remediation: "provision this machine first with `hao init --mode pair`",
		Details:     map[string]any{missing + "Path": path},
		ExitStatus:  1,
	}
}

// planPairGatewayRestart reports the service-manager plan without probing or
// mutating anything, matching init's dry-run contract (discovery only).
func planPairGatewayRestart(ctx context.Context, deps Deps, nonInteractive bool) *pairRestartReport {
	target, _ := deps.Observer.CurrentUser()
	_, support := discoverServiceManager(ctx, deps, target, nonInteractive)
	report := &pairRestartReport{Supported: support.Supported, Manager: support.Manager, Reason: support.Reason, Manual: support.Manual}
	if report.Reason == "" {
		report.Reason = "dry-run: no service changes were made"
	}
	return report
}

// restartPairGateway brings the gateway service back up so it loads the new
// passcode hash. The rotation itself is never rolled back: a restart failure
// is surfaced after the pairing string was already emitted, mirroring the
// legacy rotate command's fail-after-rotate contract. A host without a
// supported service manager (or without the unit loaded, e.g. a restored host
// before services are re-enabled) is a successful rotation with manual
// commands, never a failure.
func restartPairGateway(ctx context.Context, deps Deps, nonInteractive bool) (*pairRestartReport, error) {
	target, _ := deps.Observer.CurrentUser()
	manager, support := discoverServiceManager(ctx, deps, target, nonInteractive)
	report := &pairRestartReport{Supported: support.Supported, Manager: support.Manager, Reason: support.Reason, Manual: support.Manual}
	if !support.Supported {
		report.Reason = support.Reason + "; restart the gateway so it loads the new passcode"
		return report, nil
	}
	states, err := manager.Status(ctx, []string{"gateway"})
	if err != nil {
		return report, rotateRestartError(err, "the gateway service state could not be read")
	}
	if len(states) == 1 && !states[0].Loaded {
		report.Reason = haoGatewayUnit + " is not loaded on this host; enable services with `hao init --mode pair` (or `hao setup`), then start the gateway"
		report.Manual = []string{"sudo /usr/bin/systemctl start " + haoGatewayUnit}
		return report, nil
	}
	result, err := manager.Restart(ctx, []string{"gateway"})
	if err != nil {
		return report, rotateRestartError(err, "the gateway could not be restarted")
	}
	report.Result = result
	states, err = manager.Status(ctx, []string{"gateway"})
	if err != nil {
		return report, rotateRestartError(err, "the gateway service state could not be read after restart")
	}
	report.States = states
	return report, nil
}

// rotateRestartError surfaces a post-rotation gateway restart failure without
// pretending the rotation did not happen. The pairing string was already
// emitted; the running gateway still accepts the old passcode until restart.
func rotateRestartError(cause error, detail string) error {
	return commandError{
		Code:        "pair_rotate_restart_failed",
		Message:     "the passcode was rotated and the pairing string was emitted, but " + detail,
		Operation:   "restart pair gateway",
		Remediation: "restart the gateway so it loads the new passcode: `sudo systemctl restart ao-gateway.service` (or `hao restart gateway` once services are enabled)",
		Details:     map[string]any{"diagnostic": safeDiagnostic(cause)},
		ExitStatus:  1,
		Cause:       cause,
	}
}

func writePairShow(cmd *cobra.Command, report pairShowReport) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintln(out, "Pair-mode identity (read-only; nothing was rotated or written)"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  Certificate: %s\n  Fingerprint: %s\n", redactedString(report.CertificatePath), redactedString(report.Fingerprint)); err != nil {
		return err
	}
	if len(report.Addresses) > 0 {
		if _, err := fmt.Fprintln(out, "  Addresses:"); err != nil {
			return err
		}
		for _, addr := range report.Addresses {
			if _, err := fmt.Fprintf(out, "    %s\n", redactedString(addr)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(out, "  Passcode: %s (%s; the plaintext is never stored and cannot be shown)\n", redactedString(report.PasscodeState), redactedString(report.PasscodePath)); err != nil {
		return err
	}
	if report.PairingHint != "" {
		if _, err := fmt.Fprintf(out, "\n%s\n", redactedString(report.PairingHint)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Done.")
	return err
}

func writePairRotate(cmd *cobra.Command, report pairRotateReport) error {
	out := cmd.OutOrStdout()
	if report.PasscodeState == "would-rotate" {
		if _, err := fmt.Fprintln(out, "Passcode rotation plan (dry-run; nothing changed)"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "  Certificate: %s\n  Fingerprint: %s\n", redactedString(report.CertificatePath), redactedString(report.Fingerprint)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "  Would rotate the passcode and print a fresh pairing string; the certificate and its fingerprint never change."); err != nil {
			return err
		}
		if report.GatewayRestart != nil {
			if _, err := fmt.Fprintf(out, "  Gateway: %s\n", redactedString(restartReason(report.GatewayRestart))); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(out, "Done.")
		return err
	}
	if _, err := fmt.Fprintln(out, "Passcode rotated. Every client on the old passcode is dropped and must paste the new pairing string below. The pinned certificate is unchanged, so the fingerprint below is identical to before."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  Fingerprint: %s\n", redactedString(report.Fingerprint)); err != nil {
		return err
	}
	if len(report.Addresses) > 0 {
		if _, err := fmt.Fprintln(out, "  Addresses:"); err != nil {
			return err
		}
		for _, addr := range report.Addresses {
			if _, err := fmt.Fprintf(out, "    %s\n", redactedString(addr)); err != nil {
				return err
			}
		}
	}
	if report.PairingString != "" {
		if _, err := fmt.Fprintf(out, "Paste this in Hosted AO:\n\n%s\n\n", report.PairingString); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "The passcode appears only inside that string and is never stored in plaintext."); err != nil {
		return err
	}
	if report.GatewayRestart != nil {
		if _, err := fmt.Fprintf(out, "Gateway: %s\n", redactedString(restartReason(report.GatewayRestart))); err != nil {
			return err
		}
		for _, manual := range report.GatewayRestart.Manual {
			if _, err := fmt.Fprintf(out, "  %s\n", redactedString(manual)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(out, "Done.")
	return err
}

func restartReason(report *pairRestartReport) string {
	if report == nil {
		return "not reported"
	}
	if !report.Supported || report.Reason != "" {
		return report.Reason
	}
	if len(report.Result.Completed) > 0 {
		changes := make([]string, 0, len(report.Result.Completed))
		for _, change := range report.Result.Completed {
			changes = append(changes, change.Operation+" "+change.Component+".service")
		}
		return "restarted (" + strings.Join(changes, ", ") + "); the new passcode is in effect"
	}
	return "restart pending"
}
