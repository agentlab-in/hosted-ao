package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

func TestDiscoverDaemonAddr_FromRunFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	if err := runfile.Write(path, runfile.Info{PID: 1, Port: 4242}); err != nil {
		t.Fatalf("runfile.Write: %v", err)
	}
	addr, ok := discoverDaemonAddr(path)
	if !ok || addr != "127.0.0.1:4242" {
		t.Fatalf("discoverDaemonAddr = (%q, %v), want (127.0.0.1:4242, true)", addr, ok)
	}
}

func TestDiscoverDaemonAddr_MissingRunFile(t *testing.T) {
	if _, ok := discoverDaemonAddr(filepath.Join(t.TempDir(), "missing.json")); ok {
		t.Fatal("expected ok=false for a missing run-file")
	}
}

func TestVMServe_MissingConfigurationIsUsageError(t *testing.T) {
	setConfigEnv(t)
	t.Setenv("AO_MACHINE_FILE", filepath.Join(t.TempDir(), "missing.json"))

	_, _, err := executeCLI(t, Deps{}, "vm", "serve",
		"--http-addr", "127.0.0.1:0", "--https-addr", "127.0.0.1:0")
	if err == nil {
		t.Fatal("expected an error when no domain/machine-id/account-id is configured")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2 (a fixable configuration problem is a usage error)", got)
	}
}

// TestVMServe_StartsAndShutsDownCleanly drives the full ao vm serve command
// (flag parsing, config resolution, handler construction, TLS server setup)
// through to Run, then cancels the command's context the way SIGTERM would
// and confirms it exits cleanly. It never completes a TLS handshake or a
// proxied request, so it never touches the network for real: --daemon-addr
// and --jwks-url point at addresses that are never dialed in this path.
func TestVMServe_StartsAndShutsDownCleanly(t *testing.T) {
	setConfigEnv(t)
	// Every identity field is supplied by flag, but Resolve still reads
	// machine.json up front; point it at a guaranteed-missing path so this
	// test never depends on (or is broken by) whatever happens to exist at
	// the real $HOME/.ao/hosted/machine.json on the machine running it.
	t.Setenv("AO_MACHINE_FILE", filepath.Join(t.TempDir(), "missing.json"))

	deps := Deps{}.withDefaults()
	var out, errOut bytes.Buffer
	deps.Out = &out
	deps.Err = &errOut

	cmd := NewRootCommand(deps)
	cmd.SetArgs([]string{
		"vm", "serve",
		"--domain", "vm.example.com",
		"--machine-id", "machine-1",
		"--account-id", "account-1",
		"--daemon-addr", "127.0.0.1:1",
		"--jwks-url", "http://127.0.0.1:1/jwks.json",
		"--cert-dir", t.TempDir(),
		"--http-addr", "127.0.0.1:0",
		"--https-addr", "127.0.0.1:0",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	time.Sleep(100 * time.Millisecond) // let both listeners bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ao vm serve returned an error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ao vm serve did not shut down after context cancellation")
	}
}

// setupHarnessDeps builds Deps for ao vm setup-harness with a fake claude on
// PATH, recording the interactive hand-off instead of performing it. The real
// hand-off is one exec.Cmd with inherited stdio (see runInteractive): it cannot
// run in CI because the harness login waits for a human to paste a code, which
// is exactly why everything else about the command is kept out of it.
func setupHarnessDeps(claudePath string, runErr error, calls *[][]string) Deps {
	return Deps{
		LookPath: func(name string) (string, error) {
			if name == "claude" && claudePath != "" {
				return claudePath, nil
			}
			return "", fmt.Errorf("%s missing", name)
		},
		RunInteractive: func(_ context.Context, name string, args ...string) error {
			*calls = append(*calls, append([]string{name}, args...))
			return runErr
		},
	}
}

func TestVMSetupHarnessRunsClaudeLoginInForeground(t *testing.T) {
	setConfigEnv(t)
	var calls [][]string
	stdout, _, err := executeCLI(t, setupHarnessDeps("/bin/claude", nil, &calls), "vm", "setup-harness", "claude")
	if err != nil {
		t.Fatalf("ao vm setup-harness claude: %v", err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "/bin/claude auth login" {
		t.Fatalf("interactive calls = %v, want one `/bin/claude auth login`", calls)
	}
	if !strings.Contains(stdout, "ao doctor") {
		t.Errorf("stdout = %q, want it to point at ao doctor for readiness", stdout)
	}
}

// TestVMSetupHarnessRejectsOtherHarnesses pins the deliberate v1 limit: any
// harness other than claude is refused outright, with the supported name in the
// message, rather than half-supported.
func TestVMSetupHarnessRejectsOtherHarnesses(t *testing.T) {
	setConfigEnv(t)
	for _, harness := range []string{"codex", "gemini", "Claude Code", ""} {
		t.Run("harness="+harness, func(t *testing.T) {
			var calls [][]string
			_, _, err := executeCLI(t, setupHarnessDeps("/bin/claude", nil, &calls), "vm", "setup-harness", harness)
			if err == nil {
				t.Fatalf("expected an error for harness %q", harness)
			}
			if got := ExitCode(err); got != 2 {
				t.Errorf("ExitCode = %d, want 2 (an unsupported harness name is misuse)", got)
			}
			if !strings.Contains(err.Error(), `"claude"`) {
				t.Errorf("error = %q, want it to name the supported harness", err)
			}
			if len(calls) != 0 {
				t.Errorf("interactive calls = %v, want none for an unsupported harness", calls)
			}
		})
	}
}

func TestVMSetupHarnessCaseAndSpaceInsensitive(t *testing.T) {
	setConfigEnv(t)
	var calls [][]string
	if _, _, err := executeCLI(t, setupHarnessDeps("/bin/claude", nil, &calls), "vm", "setup-harness", " Claude "); err != nil {
		t.Fatalf("ao vm setup-harness \" Claude \": %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("interactive calls = %v, want one", calls)
	}
}

func TestVMSetupHarnessRequiresExactlyOneHarness(t *testing.T) {
	setConfigEnv(t)
	for _, args := range [][]string{{"vm", "setup-harness"}, {"vm", "setup-harness", "claude", "codex"}} {
		var calls [][]string
		_, _, err := executeCLI(t, setupHarnessDeps("/bin/claude", nil, &calls), args...)
		if err == nil {
			t.Fatalf("expected an error for %v", args)
		}
		if got := ExitCode(err); got != 2 {
			t.Errorf("ExitCode for %v = %d, want 2", args, got)
		}
	}
}

func TestVMSetupHarnessErrorsWhenClaudeMissing(t *testing.T) {
	setConfigEnv(t)
	var calls [][]string
	_, _, err := executeCLI(t, setupHarnessDeps("", nil, &calls), "vm", "setup-harness", "claude")
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("err = %v, want a not-found-in-PATH failure", err)
	}
	if len(calls) != 0 {
		t.Errorf("interactive calls = %v, want none when claude is missing", calls)
	}
}

// TestVMRotatePasscode_RotatesRestartsAndInvalidatesTheOldOne is the rotate
// command's core contract: it changes the persisted passcode, restarts the
// gateway unit so the change actually takes effect (the running gateway only
// ever loads the hash once, at startup), and the old passcode's hash no
// longer matches what ends up on disk.
func TestVMRotatePasscode_RotatesRestartsAndInvalidatesTheOldOne(t *testing.T) {
	setConfigEnv(t)
	dir := t.TempDir()
	oldPasscode, err := vmgateway.GeneratePasscode(dir)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	oldHash := readSoleFile(t, dir)

	// --cert-dir points rotate-passcode at a temp directory rather than
	// letting it fall back to the real state root: without this, resolving
	// the certificate directory would touch (and create a certificate
	// under) whatever machine happens to run this test.
	certDir := t.TempDir()
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	restore := pairInterfaceIPs
	t.Cleanup(func() { pairInterfaceIPs = restore })
	pairInterfaceIPs = func() ([]string, []string) { return []string{"192.168.1.20"}, nil }

	var calls []string
	var mu sync.Mutex
	deps := Deps{
		// A real HTTP client would let the pairing string's public-address
		// probe reach the internet; this keeps the test hermetic.
		HTTPClient: &http.Client{Transport: errRoundTripper{}},
		CommandOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
			mu.Unlock()
			return nil, nil
		},
	}
	stdout, _, err := executeCLI(t, deps, "vm", "rotate-passcode", "--passcode-dir", dir, "--cert-dir", certDir)
	if err != nil {
		t.Fatalf("ao vm rotate-passcode: %v", err)
	}
	if !strings.Contains(stdout, "Passcode rotated") {
		t.Errorf("stdout = %q, want the rotation confirmation", stdout)
	}

	newHash := readSoleFile(t, dir)
	if bytes.Equal(newHash, oldHash) {
		t.Fatal("rotate-passcode must change the persisted hash")
	}
	if mobilebridge.PasswordMatches(string(newHash), oldPasscode) {
		t.Fatal("the old passcode must no longer verify against the rotated hash")
	}

	mu.Lock()
	got := strings.Join(calls, "\n")
	mu.Unlock()
	if !strings.Contains(got, "systemctl restart "+setupVMGatewayUnit) {
		t.Errorf("rotate-passcode must restart the gateway so the new passcode takes effect (the running gateway only loads it once, at startup): calls = %v", calls)
	}
}

// TestVMRotatePasscode_PrintsAValidPairingString is the credential-rule
// regression for rotation: the new passcode can only ever be handed to the
// desktop app as part of a fresh ao-pair:// string (rotation prints no
// standalone passcode the desktop could use on its own), so the output must
// contain one, on its own line, prefixed "Paste this in Hosted AO:", that
// pairstring.Validate accepts.
func TestVMRotatePasscode_PrintsAValidPairingString(t *testing.T) {
	setConfigEnv(t)
	passcodeDir := t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	certDir := t.TempDir()
	if _, err := vmgateway.LoadOrCreatePairCertificate(certDir); err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	restore := pairInterfaceIPs
	t.Cleanup(func() { pairInterfaceIPs = restore })
	pairInterfaceIPs = func() ([]string, []string) { return []string{"192.168.1.20"}, nil }

	deps := Deps{
		HTTPClient: &http.Client{Transport: errRoundTripper{}},
		CommandOutput: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		},
	}
	stdout, _, err := executeCLI(t, deps, "vm", "rotate-passcode", "--passcode-dir", passcodeDir, "--cert-dir", certDir)
	if err != nil {
		t.Fatalf("ao vm rotate-passcode: %v", err)
	}

	const prefix = "Paste this in Hosted AO:\n\n  "
	idx := strings.Index(stdout, prefix)
	if idx == -1 {
		t.Fatalf("stdout is missing the %q line:\n%s", prefix, stdout)
	}
	line := strings.SplitN(stdout[idx+len(prefix):], "\n", 2)[0]
	if err := pairstring.Validate(line); err != nil {
		t.Errorf("pairing string %q failed pairstring.Validate: %v", line, err)
	}
	if !strings.Contains(line, "192.168.1.20:443") {
		t.Errorf("pairing string = %q, want it to contain the stubbed private address", line)
	}
}

// TestVMRotatePasscode_NeverCreatesACertificateWhenOneIsMissing pins the
// same guarantee as TestPairShow_NeverCreatesACertificateWhenOneIsMissing:
// rotate-passcode's own help text promises "the pinned certificate is
// unaffected", so an empty --cert-dir must never cause this command to mint
// one, even though the pairing string it would otherwise print is silently
// skipped as a result.
func TestVMRotatePasscode_NeverCreatesACertificateWhenOneIsMissing(t *testing.T) {
	setConfigEnv(t)
	passcodeDir := t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	certDir := t.TempDir() // deliberately left empty

	deps := Deps{
		HTTPClient: &http.Client{Transport: errRoundTripper{}},
		CommandOutput: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		},
	}
	stdout, _, err := executeCLI(t, deps, "vm", "rotate-passcode", "--passcode-dir", passcodeDir, "--cert-dir", certDir)
	if err != nil {
		t.Fatalf("ao vm rotate-passcode: %v", err)
	}
	if strings.Contains(stdout, "Paste this in Hosted AO") {
		t.Errorf("must not print a pairing string when no certificate could be safely loaded:\n%s", stdout)
	}
	entries, readErr := os.ReadDir(certDir)
	if readErr != nil {
		t.Fatalf("ReadDir(%s): %v", certDir, readErr)
	}
	if len(entries) != 0 {
		t.Errorf("rotate-passcode created files in the certificate directory: %v (the pinned certificate must be unaffected)", entries)
	}
}

// TestVMRotatePasscode_NoExistingPasscodeFails confirms the command refuses
// cleanly, rather than silently provisioning a first passcode, when no box
// has ever been paired at this directory: only ao setup-vm --pair generates
// the first one.
func TestVMRotatePasscode_NoExistingPasscodeFails(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "vm", "rotate-passcode", "--passcode-dir", t.TempDir())
	if err == nil {
		t.Fatal("expected an error when no passcode has ever been provisioned")
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 (a runtime precondition, not CLI misuse)", got)
	}
}

// TestVMSetupHarnessSurfacesLoginFailure covers the user abandoning the login
// (Ctrl-C) or the harness exiting non-zero: the command must fail rather than
// claim the machine is ready.
func TestVMSetupHarnessSurfacesLoginFailure(t *testing.T) {
	setConfigEnv(t)
	var calls [][]string
	_, _, err := executeCLI(t, setupHarnessDeps("/bin/claude", errors.New("exit status 130"), &calls), "vm", "setup-harness", "claude")
	if err == nil || !strings.Contains(err.Error(), "exit status 130") {
		t.Fatalf("err = %v, want the harness exit surfaced", err)
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 (a failed login is a runtime failure, not misuse)", got)
	}
}
