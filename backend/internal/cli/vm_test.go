package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
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
	// the real $HOME/.ao/machine.json on the machine running it.
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
