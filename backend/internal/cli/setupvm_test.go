package cli

// Command-level tests for `ao setup-vm`. They deliberately never reach the
// install: the point of these is the two refusal paths, which are the ones that
// must hold on every platform in the CLI E2E matrix. What the command does
// after preflight passes is decided by the pure functions in
// setupvm_plan_test.go.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// errRoundTripper fails every HTTP request, so a test can never depend on the
// network being there (or reach out to it by accident).
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// recordingDeps captures every command the CLI would have run.
func recordingDeps(t *testing.T) (Deps, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	deps := Deps{
		HTTPClient: &http.Client{Transport: errRoundTripper{}},
		CommandOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
			mu.Unlock()
			return nil, errors.New("command not available in tests")
		},
	}
	return deps, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func TestSetupVM_RequiresADomain(t *testing.T) {
	deps, calls := recordingDeps(t)
	_, _, err := executeCLI(t, deps, "setup-vm")
	if err == nil {
		t.Fatal("expected an error when --domain is missing")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2 (a missing flag is CLI misuse)", got)
	}
	assertNoSetupMutation(t, calls())
}

func TestSetupVM_RejectsADomainThatCannotHoldACertificate(t *testing.T) {
	for _, domain := range []string{"203.0.113.10", "localhost", "vm.example.com:443"} {
		t.Run(domain, func(t *testing.T) {
			deps, calls := recordingDeps(t)
			_, _, err := executeCLI(t, deps, "setup-vm", "--domain", domain)
			if err == nil {
				t.Fatalf("expected --domain %q to be rejected", domain)
			}
			if got := ExitCode(err); got != 2 {
				t.Errorf("ExitCode = %d, want 2", got)
			}
			assertNoSetupMutation(t, calls())
		})
	}
}

// TestSetupVM_RefusesNonUbuntuWithTheManualPath is the platform gate seen from
// the outside: on macOS and Windows the command must print the manual path and
// exit rather than half-installing anything.
func TestSetupVM_RefusesNonUbuntuWithTheManualPath(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the gate only refuses non-Linux hosts here; Linux is covered by checkSetupPlatform's table test")
	}
	deps, calls := recordingDeps(t)
	out, _, err := executeCLI(t, deps, "setup-vm", "--domain", "vm.example.com")
	if err == nil {
		t.Fatal("expected ao setup-vm to refuse to run on " + runtime.GOOS)
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1: an unsupported platform is a runtime refusal, not misuse", got)
	}
	var unsupported errUnsupportedPlatform
	if !errors.As(err, &unsupported) {
		t.Errorf("err = %v, want errUnsupportedPlatform", err)
	}
	for _, want := range []string{"nothing was changed", "ao vm serve --domain vm.example.com", "gh auth login"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal output is missing %q:\n%s", want, out)
		}
	}
	assertNoSetupMutation(t, calls())
}

// TestSetupVM_FailedPreflightChangesNothing is the guarantee the whole command
// is built around. The domain cannot resolve (.invalid is reserved for exactly
// this, RFC 2606), so preflight must stop before any mutation.
func TestSetupVM_FailedPreflightChangesNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("preflight is only reached on Linux; the platform gate refuses earlier elsewhere")
	}
	deps, calls := recordingDeps(t)
	out, _, err := executeCLI(t, deps,
		"setup-vm",
		"--domain", "setup-vm-preflight.invalid",
		// Supplied so the run never needs to look its own address up over HTTP.
		"--public-ip", "203.0.113.10",
		"--reachability-probe-url", "",
	)
	if err == nil {
		t.Fatal("expected preflight to fail for a domain that cannot resolve here")
	}
	if !strings.Contains(out, "changed") {
		t.Errorf("output must state that nothing was changed:\n%s", out)
	}
	assertNoSetupMutation(t, calls())
}

// TestDiscoverPublicIPPrefersAnIPv4Answer covers the dual-stack false mismatch:
// on a box with both families the address an endpoint reports is whichever one
// the request went out over, and a v6 answer read against a perfectly good A
// record is a DNS mismatch that does not exist.
func TestDiscoverPublicIPPrefersAnIPv4Answer(t *testing.T) {
	answers := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
	}
	v6 := answers("2001:db8::1\n")
	defer v6.Close()
	v4 := answers("203.0.113.10\n")
	defer v4.Close()

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{}}.withDefaults()}

	restore := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restore })

	// The v6 endpoint answers first and is still not the one that wins.
	setupVMPublicIPEndpoints = []string{v6.URL, v4.URL}
	got, err := c.discoverPublicIP(context.Background())
	if err != nil {
		t.Fatalf("discoverPublicIP err = %v", err)
	}
	if got != "203.0.113.10" {
		t.Errorf("discoverPublicIP = %q, want the IPv4 answer: a v6 answer against an A record reports a mismatch that does not exist", got)
	}

	// A genuinely IPv6-only box finds no v4 answer anywhere and keeps the one it
	// has, so nothing is lost by looking.
	setupVMPublicIPEndpoints = []string{v6.URL}
	got, err = c.discoverPublicIP(context.Background())
	if err != nil {
		t.Fatalf("discoverPublicIP err = %v", err)
	}
	if got != "2001:db8::1" {
		t.Errorf("discoverPublicIP = %q, want the IPv6 answer when that is all there is", got)
	}
}

// assertNoSetupMutation fails if the command ran anything that could have
// modified the machine. Read-only probes are fine; the install verbs are not.
func assertNoSetupMutation(t *testing.T, calls []string) {
	t.Helper()
	mutations := []string{"apt-get", "install ", "systemctl enable", "systemctl start", "systemctl restart", "daemon-reload", "sudo -n install"}
	for _, call := range calls {
		for _, mutation := range mutations {
			if strings.Contains(call, mutation) {
				t.Errorf("nothing may be mutated on this path, but the command ran: %s", call)
			}
		}
	}
}
