package cli

// Command-level tests for `ao setup-vm`. They deliberately never reach the
// install: the point of these is the two refusal paths, which are the ones that
// must hold on every platform in the CLI E2E matrix. What the command does
// after preflight passes is decided by the pure functions in
// setupvm_plan_test.go.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
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

// ---------------------------------------------------------------------------
// writeSetupFile atomicity
// ---------------------------------------------------------------------------

// fakePrivilegedFileOps is a CommandOutput fake standing in for `install`,
// `mv`, and `rm`, run for real against a local directory instead of over SSH
// on the target VM. It also unwraps the `sudo -n <cmd> ...` prefix
// runSetupPrivileged adds whenever the test process is not root, so the
// assertions below hold on a CI runner started either way.
func fakePrivilegedFileOps(t *testing.T) (Deps, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		mu.Unlock()
		if name == "sudo" {
			if len(args) < 2 || args[0] != "-n" {
				return nil, fmt.Errorf("unexpected sudo invocation: %v", args)
			}
			name, args = args[1], args[2:]
		}
		switch name {
		case "install":
			// -m mode -o root -g root src dst
			if len(args) < 2 {
				return nil, fmt.Errorf("install: too few arguments: %v", args)
			}
			src, dst := args[len(args)-2], args[len(args)-1]
			data, err := os.ReadFile(src)
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(dst, data, 0o644)
		case "mv":
			// -f src dst
			if len(args) != 3 {
				return nil, fmt.Errorf("mv: want -f src dst, got %v", args)
			}
			return nil, os.Rename(args[1], args[2])
		case "rm":
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}
	deps := Deps{HTTPClient: &http.Client{Transport: errRoundTripper{}}, CommandOutput: run}
	return deps.withDefaults(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// lastArg returns the last whitespace-separated token of a recorded call,
// which for install and mv is always the destination path.
func lastArg(call string) string {
	fields := strings.Fields(call)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// withoutSudoPrefix strips the "sudo -n " runSetupPrivileged adds when the
// test process is not root, so an assertion about which real command ran
// holds the same way on a root and a non-root CI runner.
func withoutSudoPrefix(call string) string {
	return strings.TrimPrefix(call, "sudo -n ")
}

// TestWriteSetupFileIsAtomic is the defect-3 regression: the old
// `install tmp dest` copied straight onto dest, so a dropped SSH session or a
// killed sudo child mid-copy left a truncated, unparsable systemd unit or apt
// source file behind. The fix has to land the privileged copy on a temp file
// next to dest and rename it into place as a separate, final step.
func TestWriteSetupFileIsAtomic(t *testing.T) {
	// dest is a path on the Ubuntu VM, so writeSetupFile builds the sibling temp
	// with `path` (always forward slashes), not `filepath`. Feeding it a Windows
	// temp dir instead makes `path.Dir` see no separator at all, collapse to ".",
	// and hand `mv` a bare relative name that resolves against the process's own
	// working directory on another drive. That is an artefact of the fake dest,
	// not a defect: setup-vm is gated to Linux long before this runs, the same
	// reason the preflight test above only runs there.
	if runtime.GOOS == "windows" {
		t.Skip("setup-vm targets a Linux VM; dest is a POSIX path and the platform gate refuses earlier on Windows")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "ao-gateway.service")

	deps, calls := fakePrivilegedFileOps(t)
	c := &commandContext{deps: deps}

	changed, err := c.writeSetupFile(context.Background(), dest, []byte("first\n"))
	if err != nil {
		t.Fatalf("writeSetupFile err = %v", err)
	}
	if !changed {
		t.Error("changed = false on the first write, want true")
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "first\n" {
		t.Fatalf("dest content = %q, err = %v; want %q", got, err, "first\n")
	}

	got := calls()
	if len(got) != 2 {
		t.Fatalf("calls = %v, want exactly an install then an mv", got)
	}
	if !strings.HasPrefix(withoutSudoPrefix(got[0]), "install ") {
		t.Errorf("first call = %q, want it to start with install", got[0])
	}
	if lastArg(got[0]) == dest {
		t.Errorf("install wrote straight to dest %q: this is exactly the non-atomic path being fixed", dest)
	}
	if !strings.HasPrefix(withoutSudoPrefix(got[1]), "mv -f ") {
		t.Errorf("second call = %q, want the final step to be an mv rename", got[1])
	}
	if lastArg(got[1]) != dest {
		t.Errorf("mv destination = %q, want %q", lastArg(got[1]), dest)
	}

	// No temp sibling may survive a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(dest) {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only %s", names, filepath.Base(dest))
	}

	// Rewriting with the same content is a no-op: nothing is shelled out to.
	deps2, calls2 := fakePrivilegedFileOps(t)
	c2 := &commandContext{deps: deps2}
	if _, err := os.ReadFile(dest); err != nil {
		t.Fatal(err)
	}
	changed, err = c2.writeSetupFile(context.Background(), dest, []byte("first\n"))
	if err != nil {
		t.Fatalf("writeSetupFile (unchanged) err = %v", err)
	}
	if changed {
		t.Error("changed = true when content is identical, want false")
	}
	if len(calls2()) != 0 {
		t.Errorf("an unchanged write must not shell out to anything, got %v", calls2())
	}

	// Different content replaces dest, atomically, the same way.
	changed, err = c.writeSetupFile(context.Background(), dest, []byte("second\n"))
	if err != nil {
		t.Fatalf("writeSetupFile (changed) err = %v", err)
	}
	if !changed {
		t.Error("changed = false when content differs, want true")
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "second\n" {
		t.Fatalf("dest content after re-write = %q, err = %v; want %q", got, err, "second\n")
	}
}

// ---------------------------------------------------------------------------
// version skew
// ---------------------------------------------------------------------------

// TestSetupVersionSkewNote is the defect-4 regression: a re-run leaves the
// daemon on the old build on purpose, so the note is the only place that
// version skew is surfaced at all, and it has to name both versions loudly
// rather than just saying "replaced".
func TestSetupVersionSkewNote(t *testing.T) {
	restore := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = restore })

	t.Run("versions differ", func(t *testing.T) {
		note := setupVersionSkewNote("1.2.2")
		for _, want := range []string{"VERSION SKEW", "1.2.2", "1.2.3", "sudo systemctl restart " + setupVMDaemonUnit} {
			if !strings.Contains(note, want) {
				t.Errorf("note is missing %q:\n%s", want, note)
			}
		}
		assertNoDashes(t, note)
	})

	t.Run("old version unknown", func(t *testing.T) {
		note := setupVersionSkewNote("")
		if strings.Contains(note, "VERSION SKEW") {
			t.Errorf("an unknown old version cannot support a skew claim:\n%s", note)
		}
		if !strings.Contains(note, "sudo systemctl restart "+setupVMDaemonUnit) {
			t.Errorf("note is missing the restart command:\n%s", note)
		}
		assertNoDashes(t, note)
	})

	t.Run("versions match", func(t *testing.T) {
		note := setupVersionSkewNote("1.2.3")
		if strings.Contains(note, "VERSION SKEW") {
			t.Errorf("identical versions are not skew:\n%s", note)
		}
	})
}

// TestSetupVM_PairRejectsDomain pins the mutual exclusivity between --pair
// and --domain at the CLI boundary: pair mode has no domain at all, so
// combining the two is refused as misuse before anything runs, mirroring
// vmgateway.Resolve's own resolvePair rejection of hosted-only fields.
func TestSetupVM_PairRejectsDomain(t *testing.T) {
	deps, calls := recordingDeps(t)
	_, _, err := executeCLI(t, deps, "setup-vm", "--pair", "--domain", "vm.example.com")
	if err == nil {
		t.Fatal("expected an error when --pair and --domain are combined")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2 (misuse)", got)
	}
	assertNoSetupMutation(t, calls())
}

// TestSetupVM_PairRefusesNonDebianWithTheManualPath is --pair's platform-gate
// refusal path, mirroring TestSetupVM_RefusesNonUbuntuWithTheManualPath: a
// failed gate must change nothing and must print the pair-specific manual
// path, not the hosted one (no --domain, no ACME, no device flow).
func TestSetupVM_PairRefusesNonDebianWithTheManualPath(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the gate only refuses non-Linux hosts here; Linux is covered by checkSetupPlatform's table test")
	}
	deps, calls := recordingDeps(t)
	out, _, err := executeCLI(t, deps, "setup-vm", "--pair")
	if err == nil {
		t.Fatal("expected ao setup-vm --pair to refuse to run on " + runtime.GOOS)
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1: an unsupported platform is a runtime refusal, not misuse", got)
	}
	var unsupported errUnsupportedPlatform
	if !errors.As(err, &unsupported) {
		t.Errorf("err = %v, want errUnsupportedPlatform", err)
	}
	for _, want := range []string{"nothing was changed", "AO_VM_PAIR=on", "gh auth login"} {
		if !strings.Contains(out, want) {
			t.Errorf("pair refusal output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--domain") {
		t.Errorf("pair refusal output must not mention --domain:\n%s", out)
	}
	assertNoSetupMutation(t, calls())
}

// TestEnsureSetupPasscode_ReRunDoesNotRotate is the single most important
// test in this file: re-running ao setup-vm --pair must never rotate the
// passcode a client has already pinned. The first call generates one; every
// call after that, against the same directory, must return generated=false
// with no plaintext and must leave the persisted hash byte-for-byte
// identical. See docs/adr/0003-pair-mode-gateway.md.
func TestEnsureSetupPasscode_ReRunDoesNotRotate(t *testing.T) {
	c := &commandContext{deps: DefaultDeps()}
	dir := t.TempDir()

	first, generated, err := c.ensureSetupPasscode(dir, nil)
	if err != nil {
		t.Fatalf("first ensureSetupPasscode: %v", err)
	}
	if !generated {
		t.Fatal("the first call against an empty directory must generate a passcode")
	}
	if first == "" {
		t.Fatal("the first call must return the plaintext passcode")
	}
	firstHash := readSoleFile(t, dir)

	for i := 0; i < 3; i++ {
		again, generatedAgain, err := c.ensureSetupPasscode(dir, nil)
		if err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
		if generatedAgain {
			t.Fatalf("re-run %d: generated = true, want false: re-running must not rotate the passcode", i)
		}
		if again != "" {
			t.Fatalf("re-run %d: plaintext = %q, want empty: an unchanged passcode must never be printed again", i, again)
		}
		if got := readSoleFile(t, dir); !bytes.Equal(got, firstHash) {
			t.Fatalf("re-run %d: the persisted passcode hash changed:\nbefore: %s\nafter:  %s", i, firstHash, got)
		}
	}

	// The originally generated passcode must still be the one that verifies,
	// proving the store was never silently rotated underneath the caller.
	if hash := mobilebridge.HashPassword(first); string(firstHash) != hash {
		t.Fatalf("the persisted hash does not match the originally generated passcode: stored %s, want hash of %q (%s)", firstHash, first, hash)
	}
}

// TestEnsureSetupPairCert_ReRunDoesNotRotate is the certificate half of the
// same guarantee: a changed fingerprint is indistinguishable from an attack
// to a client that pinned the old one, so re-running must load the exact
// same certificate rather than generating a new one.
func TestEnsureSetupPairCert_ReRunDoesNotRotate(t *testing.T) {
	c := &commandContext{deps: DefaultDeps()}
	dir := t.TempDir()

	first, err := c.ensureSetupPairCert(dir, nil)
	if err != nil {
		t.Fatalf("first ensureSetupPairCert: %v", err)
	}
	firstFingerprint, err := vmgateway.PairFingerprint(first)
	if err != nil {
		t.Fatalf("PairFingerprint: %v", err)
	}
	before := readAllFiles(t, dir)

	for i := 0; i < 3; i++ {
		again, err := c.ensureSetupPairCert(dir, nil)
		if err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
		fingerprint, err := vmgateway.PairFingerprint(again)
		if err != nil {
			t.Fatalf("re-run %d: PairFingerprint: %v", i, err)
		}
		if fingerprint != firstFingerprint {
			t.Fatalf("re-run %d: fingerprint changed from %s to %s: re-running must load the same certificate", i, firstFingerprint, fingerprint)
		}
		after := readAllFiles(t, dir)
		for name, want := range before {
			if got := after[name]; !bytes.Equal(got, want) {
				t.Fatalf("re-run %d: %s changed on disk, want byte-identical", i, name)
			}
		}
	}
}

func TestChownSetupTree_NoOpWhenNotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("this test asserts the not-root no-op path; it cannot run as root")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := &user.User{Uid: "1", Gid: "1", Username: "someone-else"}
	if err := chownSetupTree(dir, owner); err != nil {
		t.Fatalf("chownSetupTree must be a no-op (and never fail) when not root: %v", err)
	}
}

// TestChownSetupDirs_NoOpWhenNotRoot mirrors TestChownSetupTree_NoOpWhenNotRoot
// for the parent-directory chown loop: neither function may fail, or touch
// anything, outside a root (sudo) invocation.
func TestChownSetupDirs_NoOpWhenNotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("this test asserts the not-root no-op path; it cannot run as root")
	}
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := &user.User{Uid: "1", Gid: "1", Username: "someone-else"}
	if err := chownSetupDirs([]string{filepath.Join(dir, "a"), nested}, owner); err != nil {
		t.Fatalf("chownSetupDirs must be a no-op (and never fail) when not root: %v", err)
	}
}

// TestSetupMissingSetupDirs_ReportsOnlyTheAncestorAboutToBeCreated is the
// regression test for the live incident: dir's real state root exists
// (~/.ao/hosted, created by an earlier run), but the immediate parent that
// GeneratePasscode/LoadOrCreatePairCertificate's own os.MkdirAll is about to
// mint (vm-gateway) does not. setupMissingSetupDirs must report exactly
// that one directory, in top-down order, and nothing that already existed.
func TestSetupMissingSetupDirs_ReportsOnlyTheAncestorAboutToBeCreated(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "home", "azureuser", ".ao", "hosted")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// vm-gateway does not exist yet; pair-passcode is the leaf a caller is
	// about to create under it, exactly the shape plan.PasscodeDir has.
	leaf := filepath.Join(stateRoot, "vm-gateway", "pair-passcode")

	got, err := setupMissingSetupDirs(leaf)
	if err != nil {
		t.Fatalf("setupMissingSetupDirs: %v", err)
	}
	want := []string{filepath.Join(stateRoot, "vm-gateway")}
	if !slices.Equal(got, want) {
		t.Fatalf("setupMissingSetupDirs(%s) = %v, want %v", leaf, got, want)
	}
	// The state root itself already existed and must never appear.
	for _, dir := range got {
		if dir == stateRoot {
			t.Fatalf("setupMissingSetupDirs reported the pre-existing state root %s as missing", stateRoot)
		}
	}
}

// TestSetupMissingSetupDirs_ReportsEveryMissingLevelOnAFreshBox covers a box
// with no prior ao setup-vm run at all, so ~/.ao itself, ~/.ao/hosted, and
// vm-gateway are all about to be minted by the same MkdirAll call. Every one
// of them must come back, outermost first, so a caller chowns them in an
// order where each directory's parent is already owned correctly by the
// time the child is reached.
func TestSetupMissingSetupDirs_ReportsEveryMissingLevelOnAFreshBox(t *testing.T) {
	home := t.TempDir() // exists; nothing under it does.
	leaf := filepath.Join(home, ".ao", "hosted", "vm-gateway", "pair-cert")

	got, err := setupMissingSetupDirs(leaf)
	if err != nil {
		t.Fatalf("setupMissingSetupDirs: %v", err)
	}
	want := []string{
		filepath.Join(home, ".ao"),
		filepath.Join(home, ".ao", "hosted"),
		filepath.Join(home, ".ao", "hosted", "vm-gateway"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("setupMissingSetupDirs(%s) = %v, want %v (outermost missing directory first)", leaf, got, want)
	}
}

// TestSetupMissingSetupDirs_ReportsNothingWhenTheParentAlreadyExists is the
// steady-state re-run case: pair-cert's own parent (vm-gateway) already
// exists from an earlier provisioning run, so there is nothing new for a
// second run to chown up the tree.
func TestSetupMissingSetupDirs_ReportsNothingWhenTheParentAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	vmGateway := filepath.Join(dir, "vm-gateway")
	if err := os.MkdirAll(vmGateway, 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(vmGateway, "pair-cert")

	got, err := setupMissingSetupDirs(leaf)
	if err != nil {
		t.Fatalf("setupMissingSetupDirs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("setupMissingSetupDirs(%s) = %v, want none: vm-gateway already existed", leaf, got)
	}
}

// readSoleFile reads the single file expected to exist directly under dir,
// without needing to know vmgateway's internal file name for it.
func readSoleFile(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("ReadDir(%s) = %d entries, want exactly 1: %v", dir, len(entries), entries)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return b
}

// readAllFiles reads every regular file directly under dir, keyed by name.
func readAllFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), err)
		}
		out[e.Name()] = b
	}
	return out
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
