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

// stubPairAddresses points pairInterfaceIPs (with only private addresses)
// and the public-IP probe at fixed, network-free values for the duration of
// the test, so address enumeration never depends on the interfaces or
// connectivity of whatever machine happens to run the suite.
func stubPairAddresses(t *testing.T, private []string) {
	t.Helper()
	stubPairInterfaceIPs(t, private, nil)

	restoreEndpoints := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restoreEndpoints })
	setupVMPublicIPEndpoints = nil
}

// stubPairInterfaceIPs overrides pairInterfaceIPs to return exactly
// (private, public) for the duration of the test.
func stubPairInterfaceIPs(t *testing.T, private, public []string) {
	t.Helper()
	restore := pairInterfaceIPs
	t.Cleanup(func() { pairInterfaceIPs = restore })
	pairInterfaceIPs = func() ([]string, []string) { return private, public }
}

// stubUnreachablePublicProbe points setupVMPublicIPEndpoints at an address
// nothing listens on, so pairPublicAddress always fails, without ever
// touching the real network.
func stubUnreachablePublicProbe(t *testing.T) {
	t.Helper()
	restore := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restore })
	setupVMPublicIPEndpoints = []string{"http://127.0.0.1:1"}
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

// TestPairPasscodeReadError_DistinguishesPermissionFromNotExist is the table
// test for pairPasscodeReadError: a not-exist (or otherwise unreadable, e.g.
// corrupt) passcode store still points at provisioning, but a permission
// error must say to re-run with sudo instead, never the other way around,
// since a permission error is proof the store is already there.
func TestPairPasscodeReadError_DistinguishesPermissionFromNotExist(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks, so this cannot simulate a permission error")
	}
	tests := []struct {
		name         string
		buildErr     func(t *testing.T) error
		wantContains string
		wantAbsent   string
	}{
		{
			name: "not exist",
			buildErr: func(t *testing.T) error {
				_, err := vmgateway.LoadPasscodeStore(t.TempDir())
				if err == nil {
					t.Fatal("expected a not-exist error against an empty directory")
				}
				return err
			},
			wantContains: "run bare `ao pair`, which provisions this box automatically",
			wantAbsent:   "re-run with sudo",
		},
		{
			name: "permission denied",
			buildErr: func(t *testing.T) error {
				dir := t.TempDir()
				if _, err := vmgateway.GeneratePasscode(dir); err != nil {
					t.Fatalf("GeneratePasscode: %v", err)
				}
				if err := os.Chmod(dir, 0); err != nil {
					t.Fatalf("Chmod: %v", err)
				}
				// t.TempDir()'s own cleanup needs to traverse and remove this
				// directory; restore it before the test ends so that succeeds.
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				_, err := vmgateway.LoadPasscodeStore(dir)
				if err == nil {
					t.Fatal("expected a permission error against a directory this uid cannot enter")
				}
				return err
			},
			wantContains: "this box is already provisioned; re-run with sudo: sudo ao pair show",
			wantAbsent:   "run bare `ao pair`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.buildErr(t)
			got := pairPasscodeReadError(err).Error()
			if !strings.Contains(got, tt.wantContains) {
				t.Errorf("pairPasscodeReadError(%v) = %q, want it to contain %q", err, got, tt.wantContains)
			}
			if strings.Contains(got, tt.wantAbsent) {
				t.Errorf("pairPasscodeReadError(%v) = %q, must not also contain %q", err, got, tt.wantAbsent)
			}
		})
	}
}

// TestPairShow_PermissionDeniedSuggestsSudo is TestPairPasscodeReadError's
// end-to-end counterpart through the real `ao pair show` command: a
// passcode directory this process cannot enter must produce the sudo
// suggestion, not the generic "run bare `ao pair`" one, which would be
// wrong twice over (the box is provisioned, and a non-root bare `ao pair`
// would hit the identical permission error).
func TestPairShow_PermissionDeniedSuggestsSudo(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks, so this cannot simulate a permission error")
	}
	passcodeDir := t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	if err := os.Chmod(passcodeDir, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(passcodeDir, 0o700) })

	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}
	_, _, err := executeCLI(t, deps, "pair", "show", "--passcode-dir", passcodeDir, "--cert-dir", t.TempDir())
	if err == nil {
		t.Fatal("expected an error: the passcode store cannot be read by this uid")
	}
	if strings.Contains(err.Error(), "run bare `ao pair`") {
		t.Errorf("err = %v, must not suggest bare `ao pair` for a permission error: the box is already provisioned and a non-root retry fails identically", err)
	}
	if !strings.Contains(err.Error(), "sudo ao pair show") {
		t.Errorf("err = %v, want it to suggest re-running with sudo ao pair show", err)
	}
}

// TestPairBare_PermissionDeniedIsTreatedAsProvisioned is bare `ao pair`'s
// counterpart: pairIsProvisioned must read a permission error as "yes, this
// box is provisioned" so runPairBare goes to runPairShow (and its sudo
// suggestion) rather than attempting to re-provision, which was the second
// half of the live bug (a non-root re-provision attempt cannot fix a
// permission problem a privileged one created).
func TestPairBare_PermissionDeniedIsTreatedAsProvisioned(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks, so this cannot simulate a permission error")
	}
	passcodeDir := t.TempDir()
	if _, err := vmgateway.GeneratePasscode(passcodeDir); err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	if err := os.Chmod(passcodeDir, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(passcodeDir, 0o700) })
	t.Setenv("AO_VM_PASSCODE_DIR", passcodeDir)
	t.Setenv("AO_VM_CERT_DIR", t.TempDir())

	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}
	_, _, err := executeCLI(t, deps, "pair")
	if err == nil {
		t.Fatal("expected an error: the passcode store cannot be read by this uid")
	}
	if strings.Contains(err.Error(), "automates Debian-family Linux only") || strings.Contains(err.Error(), "preflight failed") {
		t.Errorf("err = %v, bare ao pair must not attempt to re-provision an already-provisioned box just because this uid cannot read its passcode store", err)
	}
	if !strings.Contains(err.Error(), "sudo ao pair show") {
		t.Errorf("err = %v, want it to fall through to pair show's own sudo suggestion", err)
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
// TestDefaultPairInterfaceIPs_ExcludesLoopback), and appends the gateway
// port to every address.
func TestPairListenAddresses_PrivateBeforePublicWithPortAppended(t *testing.T) {
	stubPairInterfaceIPs(t, []string{"10.0.0.5", "192.168.1.20"}, nil)

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
	stubPairInterfaceIPs(t, []string{"10.0.0.5"}, nil)
	stubUnreachablePublicProbe(t)

	got := pairListenAddresses(context.Background(), &http.Client{Transport: errRoundTripper{}}, ":443")
	want := []string{"10.0.0.5:443"}
	if !slices.Equal(got, want) {
		t.Fatalf("pairListenAddresses = %v, want %v", got, want)
	}
}

// A box whose only address is a directly bound public IP (no NAT, no
// private interface address at all) must still show up: private-first is
// an ordering preference, not a filter that discards public interface
// addresses. This holds even when the public-IP probe itself fails, which
// is the exact scenario the private-only filter used to break: the probe
// failing must never be the reason no address (and so no pairing string)
// is produced when a perfectly usable one is already bound to an
// interface.
func TestPairListenAddresses_PublicInterfaceAddressSurvivesEvenWhenProbeFails(t *testing.T) {
	stubPairInterfaceIPs(t, nil, []string{"203.0.113.5"})
	stubUnreachablePublicProbe(t)

	got := pairListenAddresses(context.Background(), &http.Client{Transport: errRoundTripper{}}, ":443")
	want := []string{"203.0.113.5:443"}
	if !slices.Equal(got, want) {
		t.Fatalf("pairListenAddresses = %v, want %v (a directly bound public address must not be discarded)", got, want)
	}
}

// The public-IP probe's answer is skipped, not duplicated, when it matches
// an address already found on an interface.
func TestPairCandidateIPs_DeduplicatesProbeAnswerAlreadyOnAnInterface(t *testing.T) {
	stubPairInterfaceIPs(t, nil, []string{"203.0.113.5"})
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "203.0.113.5\n")
	}))
	defer pub.Close()
	restoreEndpoints := setupVMPublicIPEndpoints
	t.Cleanup(func() { setupVMPublicIPEndpoints = restoreEndpoints })
	setupVMPublicIPEndpoints = []string{pub.URL}

	got := pairCandidateIPs(context.Background(), &http.Client{})
	want := []string{"203.0.113.5"}
	if !slices.Equal(got, want) {
		t.Fatalf("pairCandidateIPs = %v, want %v (no duplicate entry)", got, want)
	}
}

// defaultPairInterfaceIPs is the one function in this file that talks to
// the real network stack (net.Interfaces); this pins the one thing that is
// safe to assert regardless of which machine runs the suite.
func TestDefaultPairInterfaceIPs_ExcludesLoopback(t *testing.T) {
	private, public := defaultPairInterfaceIPs()
	for _, addr := range append(append([]string{}, private...), public...) {
		if strings.HasPrefix(addr, "127.") || addr == "::1" {
			t.Errorf("defaultPairInterfaceIPs() included %v, must not include loopback", addr)
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

// pairSummaryAddresses is ao setup-vm --pair's single enumeration point:
// this pins that the "Addresses:" display list and the addresses embedded
// in the pairing string can never diverge, because both come from the same
// pairCandidateIPs call. Every address in the display list must appear
// (with the gateway port) inside the pairing string, and vice versa.
func TestPairSummaryAddresses_DisplayListMatchesPairingStringAddresses(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairInterfaceIPs(t, []string{"192.168.1.20"}, []string{"203.0.113.5"})
	stubUnreachablePublicProbe(t)

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	addrs, pairingString := c.pairSummaryAddresses(context.Background(), cert, "AB12CD34", true)

	want := []string{"192.168.1.20", "203.0.113.5"}
	if !slices.Equal(addrs, want) {
		t.Fatalf("addrs = %v, want %v", addrs, want)
	}
	if err := pairstring.Validate(pairingString); err != nil {
		t.Fatalf("pairstring.Validate(%q) = %v", pairingString, err)
	}
	for _, ip := range addrs {
		hostPort := ip + ":443"
		if !strings.Contains(pairingString, hostPort) {
			t.Errorf("pairing string %q is missing display address %s: display list and pairing string must come from the same enumeration", pairingString, hostPort)
		}
	}
}

// Bare interface-address enumeration with a public-only interface and a
// failing public-IP probe (the exact scenario a private-only filter used to
// silently break, per the review finding this test guards against): the
// display list still contains the interface's public address, and a
// pairing string is still produced from it, rather than the two going
// silent together.
func TestPairSummaryAddresses_PublicOnlyInterfaceWithFailedProbeStillBuildsAString(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairInterfaceIPs(t, nil, []string{"203.0.113.5"})
	stubUnreachablePublicProbe(t)

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	addrs, pairingString := c.pairSummaryAddresses(context.Background(), cert, "AB12CD34", true)

	if !slices.Equal(addrs, []string{"203.0.113.5"}) {
		t.Fatalf("addrs = %v, want [203.0.113.5]", addrs)
	}
	if pairingString == "" {
		t.Fatal("pairingString is empty, want a string built from the directly bound public interface address")
	}
	if err := pairstring.Validate(pairingString); err != nil {
		t.Fatalf("pairstring.Validate(%q) = %v", pairingString, err)
	}
	if !strings.Contains(pairingString, "203.0.113.5:443") {
		t.Errorf("pairing string %q is missing the interface address", pairingString)
	}
}

// When there really is no address at all, pairSummaryAddresses reports an
// empty pairing string (not a panic, not a malformed string), and the
// render layer (TestRenderPairCredentials_ExplainsWhenNoAddressWasFound)
// is what turns that into an explicit line instead of a silent omission.
func TestPairSummaryAddresses_NoAddressAtAllReturnsEmptyPairingString(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairInterfaceIPs(t, nil, nil)
	stubUnreachablePublicProbe(t)

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	addrs, pairingString := c.pairSummaryAddresses(context.Background(), cert, "AB12CD34", true)
	if len(addrs) != 0 {
		t.Fatalf("addrs = %v, want none", addrs)
	}
	if pairingString != "" {
		t.Fatalf("pairingString = %q, want empty", pairingString)
	}
}

// Not generated (a re-run, no fresh passcode known) never builds a pairing
// string, regardless of what addresses are available.
func TestPairSummaryAddresses_NotGeneratedNeverBuildsAPairingString(t *testing.T) {
	_, certDir, _ := pairFixture(t)
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	stubPairInterfaceIPs(t, []string{"192.168.1.20"}, nil)
	stubUnreachablePublicProbe(t)

	c := &commandContext{deps: Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}}.withDefaults()}
	addrs, pairingString := c.pairSummaryAddresses(context.Background(), cert, "", false)
	if len(addrs) == 0 {
		t.Fatal("addrs must still be populated, for the display list, even when not generated")
	}
	if pairingString != "" {
		t.Fatalf("pairingString = %q, want empty when nothing was generated this run", pairingString)
	}
}
