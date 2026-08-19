package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// pairFixture provisions a passcode and a certificate under two fresh temp
// directories, the same shape ao setup-vm --pair leaves behind, and returns
// both directories plus the certificate's human-eyeball fingerprint for
// assertions.
func pairFixture(t *testing.T) (passcodeDir, certDir, fingerprint string) {
	t.Helper()
	passcodeDir = t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	certDir = t.TempDir()
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	fingerprint, err = vmgateway.PairFingerprint(cert)
	if err != nil {
		t.Fatalf("PairFingerprint: %v", err)
	}
	return passcodeDir, certDir, fingerprint
}

// stubPairAddresses points pairPrivateAddrIPs and the public-IP probe at
// fixed, network-free values for the duration of the test, so address
// enumeration never depends on the interfaces or connectivity of whatever
// machine happens to run the suite.
func stubPairAddresses(t *testing.T, private []string) {
	t.Helper()
	restorePriv := pairPrivateAddrIPs
	t.Cleanup(func() { pairPrivateAddrIPs = restorePriv })
	pairPrivateAddrIPs = func() []string { return private }

	restoreEndpoints := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restoreEndpoints })
	setupVMPublicIPEndpoints = nil
}

// (a) ao pair show, with a stubbed cert and passcode already on disk, prints
// the fingerprint and the enumerated addresses, and exits 0.
func TestPairShow_PrintsFingerprintAndAddresses(t *testing.T) {
	passcodeDir, certDir, fingerprint := pairFixture(t)
	stubPairAddresses(t, []string{"192.168.1.20", "10.0.0.5"})

	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}
	stdout, _, err := executeCLI(t, deps, "pair", "show", "--passcode-dir", passcodeDir, "--cert-dir", certDir)
	if err != nil {
		t.Fatalf("ao pair show: %v", err)
	}
	for _, want := range []string{"Addresses:", "10.0.0.5:443", "192.168.1.20:443", "Fingerprint:", fingerprint, "ao vm rotate-passcode"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("ao pair show output is missing %q:\n%s", want, stdout)
		}
	}
}

// (b) ao pair show against a box with no pair-mode state on disk exits 1
// with a message pointing at provisioning, rather than silently succeeding
// with an empty/zero result.
func TestPairShow_NotProvisionedExitsOneAndPointsAtProvisioning(t *testing.T) {
	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}
	_, _, err := executeCLI(t, deps, "pair", "show", "--passcode-dir", t.TempDir(), "--cert-dir", t.TempDir())
	if err == nil {
		t.Fatal("expected an error when this box has never been provisioned for pair mode")
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 (a runtime precondition, not CLI misuse)", got)
	}
	if !strings.Contains(err.Error(), "provision") {
		t.Errorf("err = %v, want it to point at provisioning this box", err)
	}
}

// ao pair show must never mutate anything, including in the corrupted-state
// case where a passcode exists but the certificate directory is empty:
// vmgateway.LoadOrCreatePairCertificate would happily mint a fresh
// certificate there, which is exactly right for provisioning but would make
// a read-only "show" command silently change this box's pinned identity.
func TestPairShow_NeverCreatesACertificateWhenOneIsMissing(t *testing.T) {
	passcodeDir := t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	certDir := t.TempDir() // deliberately left empty

	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}
	_, _, err := executeCLI(t, deps, "pair", "show", "--passcode-dir", passcodeDir, "--cert-dir", certDir)
	if err == nil {
		t.Fatal("expected an error when the certificate directory is empty")
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
	entries, readErr := os.ReadDir(certDir)
	if readErr != nil {
		t.Fatalf("ReadDir(%s): %v", certDir, readErr)
	}
	if len(entries) != 0 {
		t.Errorf("ao pair show created files in the certificate directory: %v (a read-only command must never mint a new certificate)", entries)
	}
}

// (c) Address enumeration orders every private (RFC 1918) interface address
// before the public-probe result, excludes loopback (implicit: the stub
// below never returns one, exercised separately by
// TestDefaultPairPrivateAddrIPs_ExcludesLoopback), and appends the gateway
// port to every address.
func TestPairListenAddresses_PrivateBeforePublicWithPortAppended(t *testing.T) {
	restorePriv := pairPrivateAddrIPs
	t.Cleanup(func() { pairPrivateAddrIPs = restorePriv })
	pairPrivateAddrIPs = func() []string { return []string{"10.0.0.5", "192.168.1.20"} }

	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "203.0.113.9\n")
	}))
	defer pub.Close()
	restoreEndpoints := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restoreEndpoints })
	setupVMPublicIPEndpoints = []string{pub.URL}

	got := pairListenAddresses(context.Background(), &http.Client{}, ":443")
	want := []string{"10.0.0.5:443", "192.168.1.20:443", "203.0.113.9:443"}
	if !slices.Equal(got, want) {
		t.Fatalf("pairListenAddresses = %v, want %v (private addresses, sorted, before the public probe result)", got, want)
	}
}

// A failed or unreachable public probe is skipped in silence: the address
// list still comes back with just the private addresses, and the command
// this feeds must never fail just because the internet was not reachable.
func TestPairListenAddresses_SkipsAnUnreachablePublicProbe(t *testing.T) {
	restorePriv := pairPrivateAddrIPs
	t.Cleanup(func() { pairPrivateAddrIPs = restorePriv })
	pairPrivateAddrIPs = func() []string { return []string{"10.0.0.5"} }

	restoreEndpoints := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restoreEndpoints })
	setupVMPublicIPEndpoints = []string{"http://127.0.0.1:1"}

	got := pairListenAddresses(context.Background(), &http.Client{Transport: errRoundTripper{}}, ":443")
	want := []string{"10.0.0.5:443"}
	if !slices.Equal(got, want) {
		t.Fatalf("pairListenAddresses = %v, want %v", got, want)
	}
}

// defaultPairPrivateAddrIPs is the one function in this file that talks to
// the real network stack (net.Interfaces); this pins the one thing that is
// safe to assert regardless of which machine runs the suite.
func TestDefaultPairPrivateAddrIPs_ExcludesLoopback(t *testing.T) {
	for _, addr := range defaultPairPrivateAddrIPs() {
		if strings.HasPrefix(addr, "127.") || addr == "::1" {
			t.Errorf("defaultPairPrivateAddrIPs() = %v, must not include loopback", addr)
		}
	}
}

// Bare `ao pair`, run against a box that already has a passcode and a
// certificate on disk, behaves exactly like `ao pair show`: no privileged
// command runs, since nothing is provisioned again.
func TestPairBare_AlreadyProvisionedBehavesLikeShow(t *testing.T) {
	passcodeDir, certDir, fingerprint := pairFixture(t)
	stubPairAddresses(t, []string{"192.168.1.20"})
	t.Setenv("AO_VM_PASSCODE_DIR", passcodeDir)
	t.Setenv("AO_VM_CERT_DIR", certDir)

	deps := Deps{
		HTTPClient: &http.Client{Transport: errRoundTripper{}},
		CommandOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("unexpected privileged command on an already-provisioned box: %s %v", name, args)
		},
	}
	stdout, _, err := executeCLI(t, deps, "pair")
	if err != nil {
		t.Fatalf("ao pair: %v", err)
	}
	for _, want := range []string{"Addresses:", "192.168.1.20:443", "Fingerprint:", fingerprint} {
		if !strings.Contains(stdout, want) {
			t.Errorf("bare ao pair output is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Passcode rotated") {
		t.Error("bare ao pair on an already-provisioned box must not rotate anything")
	}
}

// Bare `ao pair`, run against a box with no pair-mode state at all,
// delegates into ao setup-vm --pair's own provisioning path rather than
// failing with pair show's "not provisioned" error: on this (non-Debian)
// test host the platform gate refuses first, and its refusal text is
// specific to the setup-vm pair path, which is the signal that delegation
// actually happened rather than pair show's own precondition check firing.
func TestPairBare_NotProvisionedDelegatesToSetupVMPairProvisioning(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the platform gate only refuses non-Linux hosts here; Linux is covered by checkSetupPlatform's own table test")
	}
	t.Setenv("AO_VM_PASSCODE_DIR", t.TempDir())
	t.Setenv("AO_VM_CERT_DIR", t.TempDir())

	deps, calls := recordingDeps(t)
	stdout, _, err := executeCLI(t, deps, "pair")
	if err == nil {
		t.Fatal("expected ao pair to refuse to provision on " + runtime.GOOS)
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1: an unsupported platform is a runtime refusal, not misuse", got)
	}
	if !strings.Contains(stdout, "ao setup-vm --pair automates Debian-family Linux only") {
		t.Errorf("stdout = %q, want the setup-vm pair-specific manual path (proof delegation reached provisioning, not pair show's own error)", stdout)
	}
	assertNoSetupMutation(t, calls())
}

// `ao pair <unknown-subcommand>` is CLI misuse, exit 2, not a runtime
// failure: cobra hands the unrecognised token to the parent's own Args
// check (noArgs), which is exactly what should reject it.
func TestPairUnknownSubcommandExitsTwo(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "pair", "frobnicate")
	if err == nil {
		t.Fatal("expected an error for an unknown ao pair subcommand")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2 (misuse)", got)
	}
}

func TestPairShowRejectsExtraArgs(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "pair", "show", "extra")
	if err == nil {
		t.Fatal("expected an error for an unexpected positional argument")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2 (misuse)", got)
	}
}

// pairstring sanity: buildPairingString must hand back something
// pairstring.Validate accepts whenever it reports ok, so any drift between
// the two packages' grammar is caught here rather than only in production.
func TestBuildPairingString_ProducesAValidString(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairAddresses(t, []string{"192.168.1.20"})

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	s, addrs, ok := c.buildPairingString(context.Background(), cert, "AB12CD34")
	if !ok {
		t.Fatal("buildPairingString ok = false, want true")
	}
	if err := pairstring.Validate(s); err != nil {
		t.Errorf("pairstring.Validate(%q) = %v", s, err)
	}
	if !slices.Equal(addrs, []string{"192.168.1.20:443"}) {
		t.Errorf("addrs = %v, want [192.168.1.20:443]", addrs)
	}
}

// buildPairingString reports ok=false, rather than an empty string plus a
// swallowed error, when there is nothing to build an address from: this is
// the guard that keeps a missing address from ever silently degrading into
// an empty "ao-pair://" the desktop app could not possibly parse.
func TestBuildPairingString_NoAddressReportsNotOK(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairAddresses(t, nil)

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	s, _, ok := c.buildPairingString(context.Background(), cert, "AB12CD34")
	if ok || s != "" {
		t.Errorf("buildPairingString = (%q, ok=%v), want (\"\", false) with no address available", s, ok)
	}
}
