package haocli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// initDeps builds a Deps whose state root is a real temp directory and whose
// configuration is read from the real filesystem, so init can write and then
// re-read the synthesized configuration. Pair identity lands in the env
// overrides the caller sets (AO_VM_CERT_DIR / AO_VM_PASSCODE_DIR); the
// defaults resolve under the real home and would touch real machine state.
func initDeps(t *testing.T, obs *fakeObserver) (Deps, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		StateDir: func() (string, error) { return root, nil },
		DataDir:  func() (string, error) { return filepath.Join(root, "data"), nil },
		RunFile:  func() (string, error) { return filepath.Join(root, "running.json"), nil },
		ReadFile: os.ReadFile,
		Observer: obs,
		Timeout:  25 * time.Millisecond,
	}
	return deps, root
}

func initConfigPath(root string) string {
	return filepath.Join(root, "hao", "config.yaml")
}

func decodeInitReport(t *testing.T, out string) initReport {
	t.Helper()
	var report initReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode init report: %v\n%s", err, out)
	}
	return report
}

func provisionedPairEnv(t *testing.T) (certDir string) {
	t.Helper()
	certDir = filepath.Join(t.TempDir(), "cert")
	passcodeDir := filepath.Join(t.TempDir(), "passcode")
	t.Setenv("AO_VM_CERT_DIR", certDir)
	t.Setenv("AO_VM_PASSCODE_DIR", passcodeDir)
	return certDir
}

// recordingExecuteSetup captures the setup plans hao init executes, so tests
// can assert init installs service definitions before it activates services.
type recordingExecuteSetup struct {
	plans []SetupPlan
	seq   *[]string
}

func (r *recordingExecuteSetup) run(_ context.Context, plan SetupPlan, _ string, _ SetupExecutionOptions) (SetupExecutionResult, error) {
	r.plans = append(r.plans, plan)
	if r.seq != nil {
		*r.seq = append(*r.seq, "setup")
	}
	return SetupExecutionResult{Status: "completed"}, nil
}

// preparedInitSetup configures obs and deps so hao init's embedded setup pass
// plans a non-blocked machine (absent artifact and service definitions, backed
// by trusted release metadata) and executes through a recording fake rather
// than touching the real filesystem.
func preparedInitSetup(t *testing.T, obs *fakeObserver, root string, deps *Deps, exec *recordingExecuteSetup) {
	t.Helper()
	trusted := ArtifactMetadata{Version: "0.14.0", SHA256: strings.Repeat("a", 64), Source: "https://github.com/agentlab-in/hosted-ao/releases/download/v0.14.0/ao-linux-x64"}
	obs.statErr[filepath.Join(root, "bin", "ao")] = os.ErrNotExist
	obs.statErr["/etc/systemd/system/ao-daemon.service"] = os.ErrNotExist
	obs.statErr["/etc/systemd/system/ao-gateway.service"] = os.ErrNotExist
	deps.TrustedArtifact = func(_, _, _ string) (ArtifactMetadata, bool) { return trusted, true }
	deps.ExecuteSetup = exec.run
}

func activeUnits(obs *fakeObserver) {
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
}

func TestInitRejectsDeferredAndInvalidModes(t *testing.T) {
	obs := healthyObserver()
	deps, _ := initDeps(t, obs)
	out, stderr, code := runCLI(t, deps, "--json", "init", "--dry-run", "--mode", "https")
	if code != 2 || !strings.Contains(stderr, "feature_deferred") || out != "" {
		t.Fatalf("https: code=%d out=%q err=%q", code, out, stderr)
	}
	out, stderr, code = runCLI(t, deps, "--json", "init", "--dry-run", "--mode", "bogus")
	if code != 2 || !strings.Contains(stderr, "invalid_usage") || strings.Contains(stderr, "must be local or pair") == false {
		t.Fatalf("bogus: code=%d out=%q err=%q", code, out, stderr)
	}
}

func TestInitRejectsModeMismatchAgainstExistingConfig(t *testing.T) {
	obs := healthyObserver()
	deps := observationDeps(t, "local", obs)
	out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "init", "--dry-run", "--mode", "pair", "--non-interactive")
	if code != 2 || stderr == "" || !strings.Contains(stderr, "init_mode_mismatch") {
		t.Fatalf("code=%d out=%q err=%q", code, out, stderr)
	}
}

func TestInitDryRunPlansFreshLocalConfigWithoutWriting(t *testing.T) {
	obs := healthyObserver()
	deps, root := initDeps(t, obs)
	configPath := initConfigPath(root)
	obs.statErr[configPath] = os.ErrNotExist

	out, stderr, code := runCLI(t, deps, "--json", "init", "--dry-run", "--non-interactive", "--machine", "box7", "--mode", "local")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	if report.SchemaVersion != 1 || report.DryRun != true || report.ConfigAction != "planned-created" || report.Machine != "box7" || report.Mode != "local" {
		t.Fatalf("report=%+v", report)
	}
	if report.Services == nil || !report.Services.Supported || report.Services.Manager != "systemd" || !strings.Contains(report.Services.Reason, "dry-run") {
		t.Fatalf("services=%+v", report.Services)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote config: %v", err)
	}
}

func TestInitCreatesLocalConfigAndActivatesServices(t *testing.T) {
	obs := healthyObserver()
	obs.runFile = nil // no live daemon, so activation is conflict-free
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	configPath := initConfigPath(root)
	obs.statErr[configPath] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, &recordingExecuteSetup{})

	out, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "local")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	if report.ConfigAction != "created" || report.Services == nil || len(report.Services.Result.Completed) != 2 {
		t.Fatalf("report=%+v", report)
	}
	want := []string{"enable " + haoDaemonUnit, "start " + haoDaemonUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
	for _, call := range commands.calls {
		if !call.privileged || call.executable != "/usr/bin/systemctl" {
			t.Fatalf("unsafe service mutation=%+v", call)
		}
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := yaml.Unmarshal(written, &object); err != nil {
		t.Fatal(err)
	}
	if configString(object, "machine", "name") != "box7" || configString(object, "mode") != "local" || configBool(object, "service", "enabled") != true {
		t.Fatalf("written config=%v", object)
	}
}

func TestInitDryRunPairDoesNotProvisionIdentity(t *testing.T) {
	certDir := provisionedPairEnv(t)
	obs := healthyObserver()
	obs.runFile = nil
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	obs.statErr[initConfigPath(root)] = os.ErrNotExist

	out, stderr, code := runCLI(t, deps, "--json", "init", "--dry-run", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	if report.ConfigAction != "planned-created" || report.Identity == nil || report.Identity.PasscodeState != "will-be-minted" {
		t.Fatalf("report=%+v", report)
	}
	if report.Identity.Fingerprint != "" || report.PairingString != "" {
		t.Fatalf("dry run fabricated credentials: identity=%+v pairing=%q", report.Identity, report.PairingString)
	}
	if len(report.Identity.Addresses) == 0 {
		t.Fatalf("dry run omitted the planned pairing addresses: %+v", report.Identity)
	}
	if _, err := os.Stat(certDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created a pair certificate: %v", err)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("dry run mutated services: %+v", commands.calls)
	}
	if _, err := os.Stat(initConfigPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote config: %v", err)
	}
}

func TestInitPairProvisionsAndPrintsMintedPairingString(t *testing.T) {
	certDir := provisionedPairEnv(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	obs := healthyObserver()
	activeUnits(obs) // the healthy run file + active daemon unit mean no supervisor conflict
	obs.tlsLeaf = cert.Certificate[0]
	obs.httpsStatus = 200
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	obs.statErr[initConfigPath(root)] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, &recordingExecuteSetup{})

	out, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	if report.Mode != "pair" || report.ConfigAction != "created" {
		t.Fatalf("report=%+v", report)
	}
	if report.Identity == nil || report.Identity.CertificateCreated || report.Identity.PasscodeState != "minted" || report.Identity.Fingerprint == "" {
		t.Fatalf("identity=%+v", report.Identity)
	}
	if report.PairingString == "" || !strings.HasPrefix(report.PairingString, "ao-pair://v1/") || pairstring.Validate(report.PairingString) != nil {
		t.Fatalf("pairing string=%q", report.PairingString)
	}
	if !strings.Contains(report.PairingString, "127.0.0.1:443") {
		t.Fatalf("pairing string lacks loopback: %q", report.PairingString)
	}
	pieces := strings.Split(report.PairingString, ":")
	if got, want := obs.httpsTokens, pieces[len(pieces)-1]; len(got) != 1 || got[0] != want {
		t.Fatalf("passcode probe token=%q, pairing passcode=%q", got, want)
	}
	byID := map[string]verificationCheck{}
	for _, check := range report.Verification {
		byID[check.ID] = check
	}
	for _, id := range []string{"ao.doctor", "gateway.tls", "gateway.passcode"} {
		if byID[id].Status != "pass" {
			t.Fatalf("check %s=%+v", id, byID[id])
		}
	}
	if got := report.Identity.Addresses; len(got) == 0 || got[0] != "127.0.0.1:443" {
		t.Fatalf("addresses=%v", got)
	}
}

func TestInitPairRerunPrintsExistingSecretOnlyOnce(t *testing.T) {
	certDir := provisionedPairEnv(t)
	obs := healthyObserver()
	obs.runFile = nil
	activeUnits(obs)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	obs.tlsLeaf = cert.Certificate[0]
	obs.httpsStatus = 200
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	configPath := initConfigPath(root)
	obs.statErr[configPath] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, &recordingExecuteSetup{})

	first, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("first: code=%d err=%q out=%s", code, stderr, first)
	}
	firstReport := decodeInitReport(t, first)
	if firstReport.PairingString == "" {
		t.Fatalf("minting run printed no pairing string")
	}
	delete(obs.statErr, configPath)

	second, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("second: code=%d err=%q out=%s", code, stderr, second)
	}
	secondReport := decodeInitReport(t, second)
	if secondReport.ConfigAction != "validated" || secondReport.Identity == nil || secondReport.Identity.PasscodeState != "existing" || secondReport.Identity.CertificateCreated {
		t.Fatalf("second report=%+v", secondReport)
	}
	if secondReport.PairingString != "" || secondReport.PairingHint == "" {
		t.Fatalf("second run must withhold the pairing string: pairing=%q hint=%q", secondReport.PairingString, secondReport.PairingHint)
	}
}

func TestInitVerificationFailureFailsSilentlyWithReportOnStdout(t *testing.T) {
	certDir := provisionedPairEnv(t)
	_, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	obs := healthyObserver()
	obs.runFile = nil
	activeUnits(obs)
	obs.tlsLeaf = []byte("unexpected-der")
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	obs.statErr[initConfigPath(root)] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, &recordingExecuteSetup{})

	out, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 1 || stderr != "" || out == "" {
		t.Fatalf("code=%d err=%q out=%q", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	found := false
	for _, check := range report.Verification {
		if check.ID == "gateway.tls" && check.Status == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("verification=%+v", report.Verification)
	}
}

func TestInitInstallsServiceDefinitionsBeforeActivation(t *testing.T) {
	certDir := provisionedPairEnv(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	obs := healthyObserver()
	obs.runFile = nil
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	obs.tlsLeaf = cert.Certificate[0]
	obs.httpsStatus = 200
	commands := &fakeServiceCommands{fail: map[string]error{}}
	seq := []string{}
	commands.afterRun = func(a string) { seq = append(seq, a) }
	exec := &recordingExecuteSetup{seq: &seq}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	obs.statErr[initConfigPath(root)] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, exec)

	out, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	if len(seq) == 0 || seq[0] != "setup" {
		t.Fatalf("setup did not run before service activation: %v", seq)
	}
	// The executed setup plan must install both service definitions.
	var plan SetupPlan
	for _, p := range exec.plans {
		plan = p
	}
	foundDaemon, foundGateway := false, false
	for _, step := range plan.Steps {
		switch step.ID {
		case "service.daemon":
			foundDaemon = step.Operation == "install-definition"
		case "service.gateway":
			foundGateway = step.Operation == "install-definition"
		}
	}
	if !foundDaemon || !foundGateway {
		t.Fatalf("setup plan did not install service definitions: %+v", plan.Steps)
	}
}

func TestInitFailedActivationRollsBackMintedPasscode(t *testing.T) {
	certDir := provisionedPairEnv(t)
	passcodeDir := os.Getenv("AO_VM_PASSCODE_DIR")
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	obs := healthyObserver()
	obs.runFile = nil
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{"enable " + haoDaemonUnit: errors.New("unit ao-daemon.service does not exist")}}
	deps, root := initDeps(t, obs)
	deps.ServiceCommands = commands
	configPath := initConfigPath(root)
	obs.statErr[configPath] = os.ErrNotExist
	preparedInitSetup(t, obs, root, &deps, &recordingExecuteSetup{})

	out, stderr, code := runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 1 {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	if _, statErr := os.Stat(filepath.Join(passcodeDir, "passcode.hash")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("passcode store was not rolled back after failed init: %v", statErr)
	}

	// A retry must mint a fresh passcode and print its pairing string once,
	// without requiring `hao pair rotate`.
	commands.fail = map[string]error{}
	activeUnits(obs)
	obs.tlsLeaf = cert.Certificate[0]
	obs.httpsStatus = 200
	delete(obs.statErr, configPath)
	out, stderr, code = runCLI(t, deps, "--json", "init", "--non-interactive", "--machine", "box7", "--mode", "pair")
	if code != 0 || stderr != "" {
		t.Fatalf("retry: code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeInitReport(t, out)
	if report.Identity == nil || report.Identity.PasscodeState != "minted" {
		t.Fatalf("retry did not mint a fresh passcode: %+v", report.Identity)
	}
	if report.PairingString == "" || !strings.HasPrefix(report.PairingString, "ao-pair://v1/") {
		t.Fatalf("retry printed no pairing string: %q", report.PairingString)
	}
}
