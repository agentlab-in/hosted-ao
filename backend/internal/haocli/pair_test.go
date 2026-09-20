package haocli

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// provisionedPairDirs sets the pair identity overrides to real temp
// directories and returns both, complementing init's provisionedPairEnv (which
// returns only the certificate dir) for tests that must read the persisted
// passcode hash.
func provisionedPairDirs(t *testing.T) (certDir, passcodeDir string) {
	t.Helper()
	certDir = filepath.Join(t.TempDir(), "cert")
	passcodeDir = filepath.Join(t.TempDir(), "passcode")
	t.Setenv("AO_VM_CERT_DIR", certDir)
	t.Setenv("AO_VM_PASSCODE_DIR", passcodeDir)
	return certDir, passcodeDir
}

func readPasscodeHash(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "passcode.hash"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func decodePairShowReport(t *testing.T, out string) pairShowReport {
	t.Helper()
	var report pairShowReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode pair show report: %v\n%s", err, out)
	}
	return report
}

func decodePairRotateReport(t *testing.T, out string) pairRotateReport {
	t.Helper()
	var report pairRotateReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode pair rotate report: %v\n%s", err, out)
	}
	return report
}

func TestPairShowPrintsIdentityWithoutRotatingOrChanging(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	hashBefore := readPasscodeHash(t, passcodeDir)
	certBefore, err := os.ReadFile(filepath.Join(certDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}

	deps, _ := initDeps(t, healthyObserver())
	first, stderr, code := runCLI(t, deps, "--json", "pair", "show")
	if code != 0 || stderr != "" {
		t.Fatalf("first show: code=%d err=%q out=%s", code, stderr, first)
	}
	report := decodePairShowReport(t, first)
	if report.SchemaVersion != 1 || report.CertificatePath != certDir || report.Fingerprint != fingerprint || report.PasscodeState != "present" || report.PairingHint == "" {
		t.Fatalf("show report=%+v", report)
	}
	if len(report.Addresses) == 0 || report.Addresses[0] != "127.0.0.1:443" {
		t.Fatalf("addresses=%v", report.Addresses)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(first), &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw["pairingString"]; leaked {
		t.Fatalf("show leaked a pairing string: %s", first)
	}
	if hashAfter := readPasscodeHash(t, passcodeDir); hashAfter != hashBefore {
		t.Fatalf("show rotated the passcode hash: %q != %q", hashAfter, hashBefore)
	}
	certAfter, err := os.ReadFile(filepath.Join(certDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(certAfter, certBefore) {
		t.Fatalf("show replaced the pair certificate")
	}

	// A second run is identical: show must never churn the identity it proves.
	second, stderr, code := runCLI(t, deps, "--json", "pair", "show")
	if code != 0 || stderr != "" {
		t.Fatalf("second show: code=%d err=%q out=%s", code, stderr, second)
	}
	secondReport := decodePairShowReport(t, second)
	if secondReport.Fingerprint != report.Fingerprint || !reflect.DeepEqual(secondReport.Addresses, report.Addresses) || secondReport.PasscodeState != report.PasscodeState {
		t.Fatalf("second run churned identity: %+v != %+v", secondReport, report)
	}
}

func TestPairShowNotProvisionedFailsWithoutMinting(t *testing.T) {
	certDir, _ := provisionedPairDirs(t)
	deps, _ := initDeps(t, healthyObserver())

	out, stderr, code := runCLI(t, deps, "--json", "pair", "show")
	if code != 1 || !strings.Contains(stderr, "pair_not_provisioned") {
		t.Fatalf("unprovisioned: code=%d out=%q err=%q", code, out, stderr)
	}
	if out != "" {
		t.Fatalf("unprovisioned show must not emit a report: %q", out)
	}
	if _, err := os.Stat(certDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("show minted identity state: %v", err)
	}

	// A certificate without a passcode store is still not provisioned: the
	// plaintext cannot be recovered, so show must refuse, not fabricate.
	certOnlyDir := filepath.Join(t.TempDir(), "cert2")
	t.Setenv("AO_VM_CERT_DIR", certOnlyDir)
	if _, err := vmgateway.LoadOrCreatePairCertificate(certOnlyDir); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runCLI(t, deps, "--json", "pair", "show")
	if code != 1 || !strings.Contains(stderr, "pair_not_provisioned") {
		t.Fatalf("cert-only: code=%d out=%q err=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(certOnlyDir, "passcode.hash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cert-only show wrote a passcode store: %v", err)
	}
}

func TestPairShowHonorsPairListenPortEnvOverride(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_VM_HTTPS_ADDR", ":8443")

	deps, _ := initDeps(t, healthyObserver())
	out, stderr, code := runCLI(t, deps, "--json", "pair", "show")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodePairShowReport(t, out)
	if len(report.Addresses) == 0 || report.Addresses[0] != "127.0.0.1:8443" {
		t.Fatalf("addresses=%v, want the AO_VM_HTTPS_ADDR port", report.Addresses)
	}
}

func TestPairRotatePrintsFreshStringKeepsFingerprintAndRestartsGateway(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	hashBefore := readPasscodeHash(t, passcodeDir)

	obs := healthyObserver()
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodePairRotateReport(t, out)
	if report.SchemaVersion != 1 || report.DryRun || report.PasscodeState != "rotated" || report.CertificatePath != certDir || report.Fingerprint != fingerprint {
		t.Fatalf("report=%+v", report)
	}
	if report.PairingString == "" || !strings.HasPrefix(report.PairingString, "ao-pair://v1/") || pairstring.Validate(report.PairingString) != nil {
		t.Fatalf("pairing string=%q", report.PairingString)
	}
	if !strings.Contains(report.PairingString, "127.0.0.1:443") || !strings.Contains(report.PairingString, pairstring.Fingerprint(leaf)) {
		t.Fatalf("pairing string lacks address or fingerprint: %q", report.PairingString)
	}
	passcode := report.PairingString[strings.LastIndex(report.PairingString, ":")+1:]
	if len(passcode) != 8 || !passcodeAlphanumeric(passcode) {
		t.Fatalf("pairing passcode=%q", passcode)
	}
	if hashAfter := readPasscodeHash(t, passcodeDir); hashAfter == hashBefore {
		t.Fatalf("rotate left the passcode hash unchanged")
	}
	if report.GatewayRestart == nil || !report.GatewayRestart.Supported || report.GatewayRestart.Manager != "systemd" {
		t.Fatalf("gateway restart=%+v", report.GatewayRestart)
	}
	want := []string{"stop " + haoGatewayUnit, "start " + haoGatewayUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
	if len(report.GatewayRestart.States) != 1 || report.GatewayRestart.States[0].Component != "gateway" || !report.GatewayRestart.States[0].Loaded || !report.GatewayRestart.States[0].Active {
		t.Fatalf("states=%+v", report.GatewayRestart.States)
	}

	// A second rotation changes the hash again but never the identity: the
	// certificate fingerprint and path are the only churn-free facts.
	hashAfter := readPasscodeHash(t, passcodeDir)
	second, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 0 || stderr != "" {
		t.Fatalf("second rotate: code=%d err=%q out=%s", code, stderr, second)
	}
	secondReport := decodePairRotateReport(t, second)
	if secondReport.Fingerprint != fingerprint || secondReport.CertificatePath != certDir || secondReport.PairingString == report.PairingString {
		t.Fatalf("second rotate churned identity or repeated the string: %+v", secondReport)
	}
	if hashThird := readPasscodeHash(t, passcodeDir); hashThird == hashAfter {
		t.Fatalf("second rotate left the passcode hash unchanged")
	}
}

func TestPairShowHumanOutput(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	deps, _ := initDeps(t, healthyObserver())

	out, stderr, code := runCLI(t, deps, "pair", "show")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	for _, want := range []string{
		"Pair-mode identity (read-only; nothing was rotated or written)",
		"Fingerprint: " + fingerprint,
		"Passcode: present",
		"the pairing string is printed only when the passcode is minted or rotated",
		"mint a fresh one from this same identity with `hao pair rotate`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human show missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "Done.\n") {
		t.Fatalf("human show must end with Done.: %q", out)
	}
}

func TestPairRotateHumanOutput(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	obs := healthyObserver()
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "pair", "rotate")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	for _, want := range []string{
		"Passcode rotated.",
		"Fingerprint: " + fingerprint,
		"Paste this in Hosted AO:",
		"ao-pair://v1/",
		"The passcode appears only inside that string and is never stored in plaintext.",
		"Gateway: ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human rotate missing %q in:\n%s", want, out)
		}
	}
	line := out[strings.Index(out, "ao-pair://v1/"):]
	pairingString := strings.SplitN(line, "\n", 2)[0]
	if pairstring.Validate(pairingString) != nil {
		t.Fatalf("human rotate pairing string invalid: %q", pairingString)
	}
	passcode := pairingString[strings.LastIndex(pairingString, ":")+1:]
	if len(passcode) != 8 || !passcodeAlphanumeric(passcode) {
		t.Fatalf("human rotate passcode=%q", passcode)
	}
	if !strings.HasSuffix(out, "Done.\n") {
		t.Fatalf("human rotate must end with Done.: %q", out)
	}
}

func passcodeAlphanumeric(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func TestPairRotateDryRunChangesNothing(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}
	hashBefore := readPasscodeHash(t, passcodeDir)

	obs := healthyObserver()
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate", "--dry-run")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodePairRotateReport(t, out)
	if report.DryRun != true || report.PasscodeState != "would-rotate" || report.PairingString != "" || report.Fingerprint != fingerprint {
		t.Fatalf("dry-run report=%+v", report)
	}
	if report.GatewayRestart == nil || !report.GatewayRestart.Supported || !strings.Contains(report.GatewayRestart.Reason, "dry-run") {
		t.Fatalf("dry-run gateway plan=%+v", report.GatewayRestart)
	}
	if hashAfter := readPasscodeHash(t, passcodeDir); hashAfter != hashBefore {
		t.Fatalf("dry-run rotated the passcode hash")
	}
	if len(commands.calls) != 0 {
		t.Fatalf("dry-run mutated services: %+v", commands.calls)
	}
}

func TestPairRotateNotProvisionedFailsBeforeAnyMutation(t *testing.T) {
	_, passcodeDir := provisionedPairDirs(t)
	deps, _ := initDeps(t, healthyObserver())

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 1 || !strings.Contains(stderr, "pair_not_provisioned") || out != "" {
		t.Fatalf("unprovisioned: code=%d out=%q err=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(passcodeDir, "passcode.hash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotate minted identity state without a certificate: %v", err)
	}

	certDir := filepath.Join(t.TempDir(), "cert2")
	t.Setenv("AO_VM_CERT_DIR", certDir)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	certBytesBefore, err := os.ReadFile(filepath.Join(certDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.PairFingerprint(cert); err != nil {
		t.Fatal(err)
	}

	out, stderr, code = runCLI(t, deps, "--json", "pair", "rotate")
	if code != 1 || !strings.Contains(stderr, "pair_not_provisioned") || out != "" {
		t.Fatalf("cert-only: code=%d out=%q err=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(passcodeDir, "passcode.hash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotate minted a passcode store without an existing one: %v", err)
	}
	certBytesAfter, err := os.ReadFile(filepath.Join(certDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(certBytesAfter, certBytesBefore) {
		t.Fatalf("failed rotate replaced the pair certificate")
	}
}

func TestPairRotateReportsManualRestartWhenUnsupported(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}

	obs := healthyObserver()
	obs.platform, obs.arch = "darwin", "arm64"
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodePairRotateReport(t, out)
	if report.PasscodeState != "rotated" || report.PairingString == "" || pairstring.Validate(report.PairingString) != nil {
		t.Fatalf("report=%+v", report)
	}
	if report.GatewayRestart == nil || report.GatewayRestart.Supported || report.GatewayRestart.Manager != "manual" || !strings.Contains(report.GatewayRestart.Reason, "Linux") || len(report.GatewayRestart.Manual) == 0 {
		t.Fatalf("gateway restart=%+v", report.GatewayRestart)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("unsupported platform mutated services: %+v", commands.calls)
	}
}

func TestPairRotateGatewayNotLoadedReportsManualStart(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}

	obs := healthyObserver()
	obs.runs[serviceStatusKey("gateway")] = "LoadState=not-found\nUnitFileState=\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodePairRotateReport(t, out)
	if report.PasscodeState != "rotated" || report.PairingString == "" {
		t.Fatalf("report=%+v", report)
	}
	if report.GatewayRestart == nil || !report.GatewayRestart.Supported || !strings.Contains(report.GatewayRestart.Reason, "not loaded") {
		t.Fatalf("gateway restart=%+v", report.GatewayRestart)
	}
	want := []string{"sudo /usr/bin/systemctl start " + haoGatewayUnit}
	if !reflect.DeepEqual(report.GatewayRestart.Manual, want) {
		t.Fatalf("manual=%v want=%v", report.GatewayRestart.Manual, want)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("not-loaded gateway was mutated: %+v", commands.calls)
	}
}

func TestPairRotateRestartFailureSurfacesPairingStringFirst(t *testing.T) {
	certDir, passcodeDir := provisionedPairDirs(t)
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatal(err)
	}
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatal(err)
	}

	obs := healthyObserver()
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{"stop " + haoGatewayUnit: errors.New("permission denied")}}
	deps, _ := initDeps(t, obs)
	deps.ServiceCommands = commands

	out, stderr, code := runCLI(t, deps, "--json", "pair", "rotate")
	if code != 1 || !strings.Contains(stderr, "pair_rotate_restart_failed") || !strings.Contains(stderr, "rotated") {
		t.Fatalf("code=%d out=%q err=%q", code, out, stderr)
	}
	report := decodePairRotateReport(t, out)
	if report.PasscodeState != "rotated" || report.PairingString == "" {
		t.Fatalf("report after failed restart=%+v", report)
	}
	if report.GatewayRestart == nil || !report.GatewayRestart.Supported {
		t.Fatalf("gateway restart=%+v", report.GatewayRestart)
	}
}

func TestPairUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"pair"},
		{"pair", "show", "extra"},
		{"pair", "rotate", "extra"},
		{"pair", "bogus"},
		{"pair", "rotate", "--nope"},
	} {
		out, stderr, code := runCLI(t, Deps{}, args...)
		if code != 2 {
			t.Fatalf("hao %v exit=%d out=%q err=%q, want usage exit 2", args, code, out, stderr)
		}
	}
}
