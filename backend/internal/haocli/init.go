package haocli

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

const initSchemaVersion = 1

// initReport is the versioned machine-readable outcome of one hao init run.
// The pairingString field intentionally bypasses output redaction: the whole
// point of provisioning is that the minted pairing string is printed exactly
// once, and it is the only secret the report carries.
type initReport struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Machine       string              `json:"machine"`
	Mode          string              `json:"mode"`
	ConfigPath    string              `json:"configPath"`
	ConfigAction  string              `json:"configAction"`
	PairingString string              `json:"pairingString,omitempty"`
	PairingHint   string              `json:"pairingHint,omitempty"`
	Identity      *pairIdentityReport `json:"identity,omitempty"`
	Services      *serviceReport      `json:"services,omitempty"`
	Verification  []verificationCheck `json:"verification,omitempty"`
	Manual        []string            `json:"manual,omitempty"`
	DryRun        bool                `json:"dryRun"`
}

type pairIdentityReport struct {
	CertificatePath    string   `json:"certificatePath"`
	CertificateCreated bool     `json:"certificateCreated"`
	Fingerprint        string   `json:"fingerprint"`
	PasscodeState      string   `json:"passcodeState"`
	Addresses          []string `json:"addresses,omitempty"`
}

type serviceReport struct {
	Supported  bool                   `json:"supported"`
	Manager    string                 `json:"manager"`
	Reason     string                 `json:"reason,omitempty"`
	Enabled    bool                   `json:"enabled"`
	Components []string               `json:"components"`
	Result     ServiceOperationResult `json:"result,omitempty"`
	States     []ManagedServiceState  `json:"states,omitempty"`
	Manual     []string               `json:"manual,omitempty"`
}

type verificationCheck struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Remediation string `json:"remediation,omitempty"`
}

func newInitCommand(deps Deps, opts *options) *cobra.Command {
	var mode, machine string
	var nonInteractive, dryRun bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize this machine for Hosted AO",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := runInit(cmd.Context(), deps, opts, initOptions{Mode: mode, Machine: machine, NonInteractive: nonInteractive, DryRun: dryRun})
			if err != nil {
				if report.Verification != nil {
					// The report carries the collected verification evidence:
					// emit it before the silent failure so consumers see why
					// init failed, matching setup's blocked-report contract.
					if opts.json {
						if writeErr := writeJSON(cmd.OutOrStdout(), report); writeErr != nil {
							return writeErr
						}
					} else if writeErr := writeInitReport(cmd, report); writeErr != nil {
						return writeErr
					}
				}
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writeInitReport(cmd, report)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "machine mode: local or pair")
	cmd.Flags().StringVar(&machine, "machine", "", "machine name (defaults to the host name)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt for input")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and print without changing the machine")
	return cmd
}

type initOptions struct {
	Mode           string
	Machine        string
	NonInteractive bool
	DryRun         bool
}

type initDesired struct {
	Path           string
	Mode           string
	Machine        string
	ServiceEnabled bool
	ConfigAction   string
	PairPort       int
}

// pairProvision carries everything provisioning learns. plaintext exists only
// in memory for the local verification probe and is never serialized into the
// report: the report's only secret is the pairing string itself.
type pairProvision struct {
	identity      *pairIdentityReport
	pairingString string
	pairingHint   string
	plaintext     string
}

func runInit(ctx context.Context, deps Deps, opts *options, io initOptions) (initReport, error) {
	report := initReport{SchemaVersion: initSchemaVersion, DryRun: io.DryRun}
	mode, err := initMode(io.Mode)
	if err != nil {
		return report, err
	}
	desired, configData, err := initConfigState(deps, opts.configPath, mode, io.Machine)
	if err != nil {
		return report, err
	}
	report.Machine, report.Mode, report.ConfigPath = desired.Machine, desired.Mode, desired.Path

	mintedPlaintext := ""
	// fail rolls back a passcode this run just minted before returning the
	// failure, so a retry mints a fresh one and prints its pairing string once
	// instead of reporting "Passcode: existing" with an unrecoverable string.
	// It never rolls back a passcode the run did not mint.
	fail := func(err error) (initReport, error) {
		if mintedPlaintext != "" && !io.DryRun {
			_ = rollbackFreshPasscode()
		}
		return report, err
	}

	if desired.Mode == "pair" {
		provision, provisionErr := provisionPairIdentity(desired.PairPort, io.DryRun)
		if provisionErr != nil {
			return report, provisionErr
		}
		report.Identity = provision.identity
		report.PairingString = provision.pairingString
		report.PairingHint = provision.pairingHint
		mintedPlaintext = provision.plaintext
	}

	report.ConfigAction = desired.ConfigAction
	if configData != nil && !io.DryRun {
		if err := writeSynthConfig(desired.Path, configData); err != nil {
			return fail(err)
		}
		report.ConfigAction = "created"
	}

	// Install the machine-preparation steps (directories, artifact, and
	// service definitions) before enabling/starting services. The installer
	// execs `hao init --mode pair` directly without running `hao setup` first,
	// so a fresh box has no /etc/systemd/system unit files yet and
	// `systemctl enable` would fail before the unit definitions exist.
	if err := prepareServiceDefinitions(ctx, deps, opts.configPath, desired, io); err != nil {
		return fail(err)
	}

	services, err := initServices(ctx, deps, desired, io)
	if err != nil {
		return fail(err)
	}
	report.Services = services
	if services != nil {
		report.Manual = services.Manual
	}

	report.Verification = verifyInit(ctx, deps, desired, services, mintedPlaintext)
	for _, check := range report.Verification {
		if check.Status == "error" {
			// A verification failure keeps the minted passcode: the gateway is
			// already running against it and the report carries the pairing
			// string, so rolling it back here would orphan a live credential.
			return report, commandError{Code: "init_failed", Message: "hao init verification failed", Operation: "initialize", Remediation: "inspect the verification failures above, then rerun hao init", Details: map[string]any{}, ExitStatus: 1, Silent: true}
		}
	}
	return report, nil
}

// prepareServiceDefinitions runs the hao setup plan and execution for the
// machine-preparation steps (directories, artifact, and service definitions)
// that must complete before hao init can enable/start services. The installer
// execs `hao init --mode pair` directly without running `hao setup` first, so
// on a fresh box the /etc/systemd/system unit files do not exist yet and
// `systemctl enable` would fail. Running setup first guarantees the unit
// definitions (and the artifact they exec) exist before activation.
//
// It only runs when a supported service manager is actually in play: on a host
// without systemd (or with service.enabled=false) initServices reports manual
// commands instead, and there are no unit definitions to install, so this step
// is a no-op and must not fail init.
func prepareServiceDefinitions(ctx context.Context, deps Deps, configPath string, desired initDesired, io initOptions) error {
	if io.DryRun || !desired.ServiceEnabled {
		return nil
	}
	target, _ := deps.Observer.CurrentUser()
	_, support := discoverServiceManager(ctx, deps, target, io.NonInteractive)
	if !support.Supported {
		return nil
	}
	path, object, err := loadConfig(deps, configPath)
	if err != nil {
		return err
	}
	setupDesired, err := resolveSetupDesired(deps, path, object, "", io.NonInteractive)
	if err != nil {
		return err
	}
	plan := planSetup(setupDesired, observeSetup(ctx, deps, setupDesired))
	if recoveryStep, found, recoveryErr := planSetupRecovery(setupDesired.StateRoot, setupDesired.DataDir); recoveryErr != nil {
		plan.Steps = append([]SetupStep{blockedStep("transaction.recovery", "setup-transaction", "inspect-recovery", "an interrupted setup journal could not be validated safely", safeDiagnostic(recoveryErr), "inspect the transaction journal manually before retrying")}, plan.Steps...)
		recountSetupPlan(&plan)
	} else if found {
		plan.Steps = append([]SetupStep{recoveryStep}, plan.Steps...)
		recountSetupPlan(&plan)
	}
	plan.DryRun = false
	if plan.Summary.Blocked > 0 {
		return commandError{Code: "setup_blocked", Message: "hao init could not prepare this machine", Operation: "prepare services", Remediation: "run `hao setup --dry-run` to review the blocked steps, resolve them, then rerun hao init", Details: map[string]any{}, ExitStatus: 1, Silent: true}
	}
	_, err = deps.ExecuteSetup(ctx, plan, setupDesired.StateRoot, SetupExecutionOptions{NonInteractive: io.NonInteractive, Input: deps.In, DataDir: setupDesired.DataDir})
	return err
}

// rollbackFreshPasscode removes the passcode store so a retry after a failed
// init mints a fresh passcode and prints its pairing string once, rather than
// reporting "Passcode: existing" with an unrecoverable plaintext.
func rollbackFreshPasscode() error {
	_, passcodeDir, err := resolvePairIdentityDirectories()
	if err != nil {
		return err
	}
	return vmgateway.RemovePasscodeStore(passcodeDir)
}

// initMode validates --mode. https remains the one deliberately deferred
// value, rejected with a stable code and remediation pointing at pair mode.
func initMode(mode string) (string, error) {
	switch mode {
	case "https":
		return "", commandError{Code: "feature_deferred", Message: "https mode is not part of the v1 configuration contract", Operation: "initialize", Remediation: "use --mode pair; https mode is deferred", Details: map[string]any{}, ExitStatus: 2}
	case "local", "pair":
		return mode, nil
	case "":
		return "local", nil
	default:
		return "", commandError{Code: "invalid_usage", Message: "--mode must be local or pair", Operation: "initialize", Remediation: "pass --mode local or --mode pair", Details: map[string]any{}, ExitStatus: 2}
	}
}

// initConfigState validates an existing configuration (requiring the requested
// mode to match) or plans one non-interactive synthesized v1 configuration
// from the environment, per the boundary spec: `hao init [--mode ...]` is the
// normal single entry point and must not require a hand-written configuration
// first. The synthesized YAML is returned for the caller to write atomically.
func initConfigState(deps Deps, explicit, mode, machineFlag string) (initDesired, []byte, error) {
	path, err := resolveConfigPath(deps, explicit)
	if err != nil {
		return initDesired{}, nil, operationalError("resolve configuration path", err)
	}
	if _, statErr := deps.Observer.Stat(path); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return initDesired{}, nil, operationalError("inspect configuration", statErr)
	} else if statErr == nil {
		loadedPath, object, err := loadConfig(deps, explicit)
		if err != nil {
			return initDesired{}, nil, err
		}
		configMode := configString(object, "mode")
		if configMode != mode {
			return initDesired{}, nil, commandError{Code: "init_mode_mismatch", Message: fmt.Sprintf("mode %s does not match the configured mode %s", mode, configMode), Operation: "initialize", Remediation: "run `hao config set mode " + mode + "` then `hao setup`, or rerun hao init without --mode", Details: map[string]any{"path": loadedPath}, ExitStatus: 2}
		}
		return initDesired{Path: loadedPath, Mode: configMode, Machine: configString(object, "machine", "name"), ServiceEnabled: configBool(object, "service", "enabled"), ConfigAction: "validated", PairPort: configInt(object, "pair", "listenPort")}, nil, nil
	}
	machine, err := initMachineName(machineFlag)
	if err != nil {
		return initDesired{}, nil, err
	}
	data, object, err := synthesizeConfig(machine, mode)
	if err != nil {
		var unsupported haocontract.UnsupportedVersionError
		if errors.As(err, &unsupported) {
			return initDesired{}, nil, commandError{Code: "unsupported_config_version", Message: "hao configuration version is unsupported", Operation: "initialize", Remediation: "use a version 1 configuration", Details: map[string]any{"path": path}, ExitStatus: 2, Cause: err}
		}
		return initDesired{}, nil, commandError{Code: "invalid_config", Message: "synthesized hao configuration is invalid", Operation: "initialize", Remediation: "pass --machine with a name matching the v1 schema", Details: map[string]any{"path": path, "diagnostic": safeDiagnostic(err)}, ExitStatus: 2, Cause: err}
	}
	return initDesired{Path: path, Mode: mode, Machine: machine, ServiceEnabled: configBool(object, "service", "enabled"), ConfigAction: "planned-created", PairPort: configInt(object, "pair", "listenPort")}, data, nil
}

// initMachineName prefers --machine and otherwise derives a schema-valid name
// from the host name, refusing silently-hostile names instead of writing an
// invalid machine name.
func initMachineName(flag string) (string, error) {
	if name := strings.TrimSpace(flag); name != "" {
		if !validMachineName(name) {
			return "", commandError{Code: "invalid_usage", Message: "--machine must be 1-63 characters of letters, digits, dots, dashes, or underscores, not starting or ending with a separator", Operation: "initialize", Remediation: "pass a machine name matching the v1 configuration schema", Details: map[string]any{}, ExitStatus: 2}
		}
		return name, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return "", operationalError("resolve host name", err)
	}
	host = strings.TrimSuffix(host, ".")
	if validMachineName(host) {
		return host, nil
	}
	sanitized := invalidNameChars.ReplaceAllString(host, "-")
	sanitized = strings.Trim(sanitized, ".-_")
	if validMachineName(sanitized) {
		return sanitized, nil
	}
	return "", commandError{Code: "invalid_usage", Message: "the host name cannot form a valid v1 machine name", Operation: "initialize", Remediation: "pass --machine with a name matching the v1 configuration schema", Details: map[string]any{"host": host}, ExitStatus: 2}
}

// namingPattern mirrors contracts/hao/v1/config.schema.json's machine.name
// regexp exactly so init never writes a name the contract rejects.
var namingPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

var invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func validMachineName(name string) bool {
	return len(name) >= 1 && len(name) <= 63 && namingPattern.MatchString(name)
}

// synthesizeConfig builds the one non-interactive default configuration the
// boundary spec promises `hao init` can produce on a fresh machine. Every
// value is fixed except the machine name and mode; run `hao config set` after
// init to diverge.
func synthesizeConfig(machine, mode string) ([]byte, map[string]any, error) {
	values := map[string]string{
		"machine.name":         machine,
		"mode":                 mode,
		"components.aoVersion": initAOVersion(),
		"harness.id":           "claude-code",
		"install.dependencies": "missing",
		"service.enabled":      "true",
		"workflow.profile":     "general",
	}
	if mode == "pair" {
		values["pair.listenPort"] = "443"
	}
	return marshalValidatedConfig(values)
}

// semverPattern enforces the components.aoVersion contract in
// contracts/hao/v1/config.schema.json (`\d` is RE2 for `[0-9]`).
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// initAOVersion returns the embedded release version when it is a valid v1
// semver pin, or a stable placeholder for non-release builds so init remains
// usable in development. The pin is corrected with `hao config set
// components.aoVersion` before first use.
func initAOVersion() string {
	if semverPattern.MatchString(AOArtifactVersion) {
		return AOArtifactVersion
	}
	return "0.0.0-dev"
}

// writeSynthConfig durably creates the synthesized configuration. It fails if
// another process created the file in the meantime, which the caller reports
// as a retry rather than an overwrite.
func writeSynthConfig(path string, data []byte) error {
	store := configMutationStore{}
	if err := store.create(path, data); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return commandError{Code: "operation_failed", Message: "another hao process created the configuration while hao init ran", Operation: "initialize", Remediation: "rerun hao init to validate the existing configuration", Details: map[string]any{"path": path, "diagnostic": safeDiagnostic(err)}, ExitStatus: 1, Cause: err}
		}
		return configWriteError(path, err)
	}
	return nil
}

// provisionPairIdentity provisions (or reports the planned provisioning of)
// the pair-mode gateway's persistent certificate and hashed passcode. The
// certificate and fingerprint are reused across runs by design
// (vmgateway.LoadOrCreatePairCertificate); the passcode plaintext exists only
// for the instant it is minted, so only the minting run can print the
// pairing string. A dry run reports the plan without creating either artifact.
func provisionPairIdentity(port int, dryRun bool) (pairProvision, error) {
	certDir, passcodeDir, err := resolvePairIdentityDirectories()
	if err != nil {
		return pairProvision{}, operationalError("resolve pair identity directories", err)
	}
	identity := &pairIdentityReport{CertificatePath: certDir}
	var leaf *x509.Certificate
	// Addresses are enumerated before the certificate is minted so the fresh
	// certificate can carry the same hints as IP Subject Alternative Names: a
	// bare-IP client that connects at one of these addresses must be able to
	// verify the SAN (Chromium enforces this where curl/OpenSSL does not).
	addresses := machineAddresses(port)
	identity.Addresses = addresses
	if !dryRun {
		identity.CertificateCreated = !vmgateway.PairCertExists(certDir)
		cert, err := vmgateway.LoadOrCreatePairCertificate(certDir, vmgateway.PairIPsFromAddresses(addresses)...)
		if err != nil {
			return pairProvision{}, operationalError("provision pair certificate", err)
		}
		leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return pairProvision{}, operationalError("parse pair certificate", err)
		}
		identity.Fingerprint, err = vmgateway.PairFingerprint(cert)
		if err != nil {
			return pairProvision{}, operationalError("render pair fingerprint", err)
		}
	}

	_, loadErr := vmgateway.LoadPasscodeStore(passcodeDir)
	if loadErr != nil && !strings.Contains(loadErr.Error(), "no passcode found") {
		return pairProvision{}, operationalError("provision pair passcode", loadErr)
	}
	plaintext := ""
	switch {
	case loadErr == nil:
		identity.PasscodeState = "existing"
	case dryRun:
		identity.PasscodeState = "will-be-minted"
	default:
		plaintext, err = vmgateway.GeneratePasscode(passcodeDir)
		if err != nil {
			return pairProvision{}, operationalError("provision pair passcode", err)
		}
		identity.PasscodeState = "minted"
	}
	pairingString, pairingHint := "", ""
	if plaintext != "" && !dryRun && leaf != nil {
		pairingString, err = pairstring.Build(addresses, pairstring.Fingerprint(leaf), plaintext)
		if err != nil {
			return pairProvision{}, operationalError("build pairing string", err)
		}
	} else if !dryRun {
		pairingHint = "the pairing string was printed when this machine was provisioned; mint a fresh one from this same identity with `hao pair rotate`"
	}
	return pairProvision{identity: identity, pairingString: pairingString, pairingHint: pairingHint, plaintext: plaintext}, nil
}

// initServices discovers and activates the machine's managed services. It
// never fails because a platform lacks a supported service manager: manual
// commands are reported instead. It does refuse to activate when a live
// daemon is already running outside the hao service manager (the desktop
// supervisor), because two daemons would fight over the loopback port.
func initServices(ctx context.Context, deps Deps, desired initDesired, io initOptions) (*serviceReport, error) {
	components := defaultLifecycleComponents(desired.Mode)
	target, _ := deps.Observer.CurrentUser()
	manager, support := discoverServiceManager(ctx, deps, target, io.NonInteractive)
	report := &serviceReport{Supported: support.Supported, Manager: support.Manager, Reason: support.Reason, Enabled: desired.ServiceEnabled, Components: components, Manual: support.Manual}
	if io.DryRun {
		if report.Reason == "" {
			report.Reason = "dry-run: no service changes were made"
		}
		return report, nil
	}
	if !desired.ServiceEnabled {
		report.Reason = "service.enabled is false; hao init does not enable or start services"
		return report, nil
	}
	if !support.Supported {
		report.Reason = support.Reason + "; run the reported manual commands"
		return report, nil
	}
	if conflict, found, err := managedDaemonConflict(ctx, deps, manager, components); err != nil {
		return report, operationalError("probe daemon supervision", err)
	} else if found {
		return report, commandError{Code: "desktop_supervised_daemon", Message: "a live AO daemon already runs outside the hao-managed services", Operation: "initialize services", Remediation: "stop the desktop-supervised daemon (or use the desktop app's own lifecycle), then rerun hao init", Details: map[string]any{"supervisor": conflict}, ExitStatus: 1}
	}
	result, err := manager.Activate(ctx, components)
	if err != nil {
		return report, err
	}
	report.Result = result
	states, err := manager.Status(ctx, components)
	if err != nil {
		return report, operationalError("read service status after activation", err)
	}
	report.States = states
	return report, nil
}

// managedDaemonConflict reports a live daemon that is running outside the
// hao-managed systemd units (the Electron desktop supervisor's daemon), which
// would fight a systemd activation on the same loopback port.
func managedDaemonConflict(ctx context.Context, deps Deps, manager *haoServiceManager, components []string) (string, bool, error) {
	runFile, err := deps.RunFile()
	if err != nil {
		return "", false, err
	}
	info, err := deps.Observer.ReadRunFile(runFile)
	//nolint:nilerr // A missing or unreadable discovery file means no live daemon: absence is not a conflict.
	if err != nil || info == nil {
		return "", false, nil
	}
	if !deps.Observer.ProcessAlive(info.PID) {
		return "", false, nil
	}
	managesDaemon := false
	for _, component := range components {
		if component == "daemon" {
			managesDaemon = true
		}
	}
	if !managesDaemon {
		return "", false, nil
	}
	states, err := manager.Status(ctx, []string{"daemon"})
	if err != nil {
		return "", false, err
	}
	if len(states) == 1 && states[0].Active {
		return "", false, nil // the live daemon is the managed systemd daemon
	}
	return runFile + " points at a live daemon outside systemd (PID " + strconv.Itoa(info.PID) + ")", true, nil
}

// verifyInit assembles read-only post-activation verification. Daemon doctor
// verification is reported, not blocking, because the daemon starts
// asynchronously; gateway TLS and passcode probes are blocking when the
// gateway is expected to be active, because a wrong pinned certificate or a
// rejected passcode would break pairing.
func verifyInit(ctx context.Context, deps Deps, desired initDesired, services *serviceReport, mintedPlaintext string) []verificationCheck {
	checks := verifyDaemon(ctx, deps)
	if desired.Mode != "pair" {
		return checks
	}
	gatewayActive := false
	if services != nil {
		for _, state := range services.States {
			if state.Component == "gateway" && state.Active {
				gatewayActive = true
			}
		}
	}
	if !gatewayActive {
		checks = append(checks, verificationCheck{ID: "gateway.tls", Status: "skipped", Evidence: "gateway service is not active; start it with `hao start` before pairing", Remediation: "run `hao start` once services are enabled"})
		return checks
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(desired.PairPort))
	probeCtx, cancel := boundedContext(ctx, deps.Timeout)
	leaf, probeErr := deps.Observer.TLSHandshake(probeCtx, addr)
	cancel()
	if probeErr != nil {
		checks = append(checks, verificationCheck{ID: "gateway.tls", Status: "error", Evidence: "TLS handshake with the pinned gateway certificate failed: " + safeDiagnostic(probeErr), Remediation: "inspect `hao logs gateway` and the gateway service state, then rerun hao init"})
		return checks
	}
	if !leafMatchesProvisionedCertificate(leaf) {
		checks = append(checks, verificationCheck{ID: "gateway.tls", Status: "error", Evidence: "the gateway presented a different certificate than the one hao provisioned", Remediation: "inspect the gateway service; a wrong certificate breaks fingerprint pinning"})
		return checks
	}
	checks = append(checks, verificationCheck{ID: "gateway.tls", Status: "pass", Evidence: "gateway presents the pinned pair certificate"})
	if mintedPlaintext == "" {
		checks = append(checks, verificationCheck{ID: "gateway.passcode", Status: "skipped", Evidence: "passcode was not minted by this run; verify pairing with the string printed at provisioning"})
		return checks
	}
	authCtx, authCancel := boundedContext(ctx, deps.Timeout)
	defer authCancel()
	status, getErr := deps.Observer.HTTPSGet(authCtx, "https://"+addr+"/", mintedPlaintext)
	if getErr != nil {
		checks = append(checks, verificationCheck{ID: "gateway.passcode", Status: "error", Evidence: "authenticated gateway probe failed: " + safeDiagnostic(getErr), Remediation: "inspect `hao logs gateway` and rerun hao init"})
		return checks
	}
	if status == 401 || status == 403 {
		checks = append(checks, verificationCheck{ID: "gateway.passcode", Status: "error", Evidence: "the gateway rejected the freshly minted passcode", Remediation: "rotate the passcode with `hao pair rotate` and re-pair"})
		return checks
	}
	checks = append(checks, verificationCheck{ID: "gateway.passcode", Status: "pass", Evidence: "the gateway accepted the freshly minted passcode"})
	return checks
}

// leafMatchesProvisionedCertificate re-reads the on-disk pair certificate and
// verifies the running gateway presents the identical leaf, the strongest
// no-network proof that fingerprint pinning will succeed.
func leafMatchesProvisionedCertificate(leaf []byte) bool {
	certPath, _, err := resolvePairIdentityDirectories()
	if err != nil {
		return false
	}
	cert, err := vmgateway.LoadOrCreatePairCertificate(certPath)
	if err != nil {
		return false
	}
	if len(cert.Certificate) == 0 {
		return false
	}
	return bytes.Equal(leaf, cert.Certificate[0])
}

func verifyDaemon(ctx context.Context, deps Deps) []verificationCheck {
	runFile, err := deps.RunFile()
	if err != nil {
		return []verificationCheck{{ID: "ao.doctor", Status: "skipped", Evidence: "daemon discovery path could not be resolved"}}
	}
	probe := observeDaemon(ctx, deps.Observer, runFile, deps.Timeout)
	if probe.Doctor == nil {
		return []verificationCheck{{ID: "ao.doctor", Status: "unknown", Evidence: "daemon is not reachable yet; run `hao doctor` once it has started", Remediation: "run hao doctor after the daemon becomes healthy"}}
	}
	if probe.Doctor.Failures > 0 || !probe.Doctor.OK {
		return []verificationCheck{{ID: "ao.doctor", Status: "error", Evidence: fmt.Sprintf("the AO daemon reported %d failing check(s)", probe.Doctor.Failures), Remediation: "run hao doctor and resolve the failing checks, then rerun hao init"}}
	}
	return []verificationCheck{{ID: "ao.doctor", Status: "pass", Evidence: "the AO daemon's runtime and terminal capabilities are healthy"}}
}

// machineAddresses lists pairing candidates, loopback first, then private,
// then public, each sorted, matching the emitter role the pairstring package
// documents (ordering is the caller's job).
func machineAddresses(port int) []string {
	var addrs []string
	addrs = append(addrs, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	seen := map[string]bool{"127.0.0.1": true}
	interfaces, err := net.Interfaces()
	if err != nil {
		return addrs
	}
	var private, public []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, _ := iface.Addrs()
		for _, addr := range list {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			joined := net.JoinHostPort(ip.String(), strconv.Itoa(port))
			if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				private = append(private, joined)
			} else {
				public = append(public, joined)
			}
		}
	}
	sort.Strings(private)
	sort.Strings(public)
	return append(append(addrs, private...), public...)
}

func writeInitReport(cmd *cobra.Command, report initReport) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Machine: %s (%s)\nConfig: %s\n  %s\n", redactedString(report.Machine), redactedString(report.Mode), redactedString(report.ConfigAction), redactedString(report.ConfigPath)); err != nil {
		return err
	}
	if report.Identity != nil {
		stateText := map[string]string{"minted": "minted (shown once below)", "existing": "existing; the pairing string is not shown again", "will-be-minted": "will be minted"}[report.Identity.PasscodeState]
		if stateText == "" {
			stateText = report.Identity.PasscodeState
		}
		if report.Identity.Fingerprint == "" {
			if _, err := fmt.Fprintf(out, "Pair certificate: %s (will be created and pinned on the real run)\n", redactedString(report.Identity.CertificatePath)); err != nil {
				return err
			}
		} else {
			created := "loaded"
			if report.Identity.CertificateCreated {
				created = "created"
			}
			if _, err := fmt.Fprintf(out, "Pair certificate: %s (%s)\n  fingerprint: %s\n", redactedString(report.Identity.CertificatePath), created, redactedString(report.Identity.Fingerprint)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(out, "Passcode: %s\n", stateText); err != nil {
			return err
		}
		if len(report.Identity.Addresses) > 0 {
			if _, err := fmt.Fprintf(out, "Pairing addresses: %s\n", redactedString(strings.Join(report.Identity.Addresses, ", "))); err != nil {
				return err
			}
		}
	}
	if report.PairingString != "" {
		if _, err := fmt.Fprintf(out, "Pairing string (shown exactly once):\n%s\n", report.PairingString); err != nil {
			return err
		}
	}
	if report.PairingHint != "" {
		if _, err := fmt.Fprintf(out, "%s\n", redactedString(report.PairingHint)); err != nil {
			return err
		}
	}
	if report.Services != nil {
		enabled := "enabled"
		if !report.Services.Enabled {
			enabled = "disabled by configuration"
		}
		if _, err := fmt.Fprintf(out, "Services: %s (%s)\n  manager: %s\n", enabled, strings.Join(report.Services.Components, ", "), report.Services.Manager); err != nil {
			return err
		}
		if report.Services.Reason != "" {
			if _, err := fmt.Fprintf(out, "  %s\n", redactedString(report.Services.Reason)); err != nil {
				return err
			}
		}
		for _, change := range report.Services.Result.Completed {
			if _, err := fmt.Fprintf(out, "  %s %s.service\n", change.Operation, change.Component); err != nil {
				return err
			}
		}
	}
	for _, check := range report.Verification {
		if _, err := fmt.Fprintf(out, "Verify %-18s %-9s %s\n", check.ID+":", check.Status, redactedString(check.Evidence)); err != nil {
			return err
		}
		if check.Remediation != "" {
			if _, err := fmt.Fprintf(out, "  remediation: %s\n", redactedString(check.Remediation)); err != nil {
				return err
			}
		}
	}
	for _, manual := range report.Manual {
		if _, err := fmt.Fprintf(out, "Manual: %s\n", redactedString(manual)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Done.")
	return err
}
