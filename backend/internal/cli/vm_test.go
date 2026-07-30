package cli

import (
	"bytes"
	"context"
	"path/filepath"
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
