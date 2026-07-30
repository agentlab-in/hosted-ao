package vmgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeMachineFile(t *testing.T, mf MachineFile) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "machine.json")
	data, err := json.Marshal(mf)
	if err != nil {
		t.Fatalf("marshal machine file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write machine file: %v", err)
	}
	return path
}

func TestResolve_FlagsOnly(t *testing.T) {
	opts := Options{
		Domain:      "vm.example.com",
		MachineID:   "machine-1",
		AccountID:   "account-1",
		MachineFile: filepath.Join(t.TempDir(), "missing.json"),
	}
	cfg, err := Resolve(opts, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Domain != "vm.example.com" || cfg.MachineID != "machine-1" || cfg.AccountID != "account-1" {
		t.Fatalf("unexpected identity fields: %+v", cfg)
	}
	if cfg.Issuer != DefaultIssuer {
		t.Errorf("Issuer = %q, want default %q", cfg.Issuer, DefaultIssuer)
	}
	if cfg.JWKSURL != DefaultIssuer+defaultJWKSPath {
		t.Errorf("JWKSURL = %q, want derived from issuer", cfg.JWKSURL)
	}
	if cfg.DaemonAddr != DefaultDaemonAddr {
		t.Errorf("DaemonAddr = %q, want default %q", cfg.DaemonAddr, DefaultDaemonAddr)
	}
	if cfg.HTTPAddr != DefaultHTTPAddr || cfg.HTTPSAddr != DefaultHTTPSAddr {
		t.Errorf("listener addrs = %q/%q, want defaults", cfg.HTTPAddr, cfg.HTTPSAddr)
	}
}

func TestResolve_MachineFileFallback(t *testing.T) {
	path := writeMachineFile(t, MachineFile{
		MachineID: "mf-machine",
		AccountID: "mf-account",
		PublicURL: "mf.example.com",
	})
	cfg, err := Resolve(Options{MachineFile: path}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Domain != "mf.example.com" || cfg.MachineID != "mf-machine" || cfg.AccountID != "mf-account" {
		t.Fatalf("machine.json fallback not applied: %+v", cfg)
	}
}

func TestResolve_FlagOverridesMachineFile(t *testing.T) {
	path := writeMachineFile(t, MachineFile{
		MachineID: "mf-machine",
		AccountID: "mf-account",
		PublicURL: "mf.example.com",
	})
	cfg, err := Resolve(Options{
		MachineFile: path,
		Domain:      "flag.example.com",
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Domain != "flag.example.com" {
		t.Errorf("Domain = %q, want flag value to win over machine.json", cfg.Domain)
	}
	if cfg.MachineID != "mf-machine" {
		t.Errorf("MachineID = %q, want machine.json fallback", cfg.MachineID)
	}
}

func TestResolve_EnvOverridesMachineFileButNotFlag(t *testing.T) {
	path := writeMachineFile(t, MachineFile{
		MachineID: "mf-machine",
		AccountID: "mf-account",
		PublicURL: "mf.example.com",
	})
	t.Setenv("AO_VM_DOMAIN", "env.example.com")
	t.Setenv("AO_VM_MACHINE_ID", "env-machine")

	cfg, err := Resolve(Options{
		MachineFile: path,
		AccountID:   "flag-account",
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Domain != "env.example.com" {
		t.Errorf("Domain = %q, want env var to win over machine.json", cfg.Domain)
	}
	if cfg.MachineID != "env-machine" {
		t.Errorf("MachineID = %q, want env var to win over machine.json", cfg.MachineID)
	}
	if cfg.AccountID != "flag-account" {
		t.Errorf("AccountID = %q, want flag to win over machine.json", cfg.AccountID)
	}
}

// machine.json's publicUrl is a full origin, but Domain is handed to
// autocert.HostWhitelist, which silently ignores anything that is not a bare
// hostname and would then never obtain a certificate.
func TestResolve_MachineFilePublicURLIsReducedToHostname(t *testing.T) {
	for _, tc := range []struct{ publicURL, want string }{
		{"https://vm.example.com", "vm.example.com"},
		{"https://vm.example.com/", "vm.example.com"},
		{"https://vm.example.com:8443", "vm.example.com"},
		{"http://vm.example.com", "vm.example.com"},
		{"vm.example.com", "vm.example.com"},
	} {
		t.Run(tc.publicURL, func(t *testing.T) {
			path := writeMachineFile(t, MachineFile{
				MachineID: "mf-machine",
				AccountID: "mf-account",
				PublicURL: tc.publicURL,
			})
			cfg, err := Resolve(Options{MachineFile: path}, t.TempDir())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if cfg.Domain != tc.want {
				t.Errorf("Domain = %q, want %q", cfg.Domain, tc.want)
			}
		})
	}
}

// This side of the publicUrl contract drops a port and keeps listening on
// DefaultHTTPSAddr; it does not carry the port into HTTPSAddr. That is only
// safe because the control plane refuses to store an origin with a port in the
// first place (normalizePublicURL in controlplane/internal/device/codes.go, and
// its TestNormalizePublicURLRejectsPort). The two normalizers are in separate
// Go modules and cannot import each other, so this test is the pin: if either
// side's port handling is changed without the other, a test fails here instead
// of a VM registering fine and then showing Offline forever, with nothing in
// the gateway log, because the desktop is calling a port nothing listens on.
func TestNormalizeDomain_PortIsDroppedNotCarried(t *testing.T) {
	got, err := normalizeDomain("https://vm.example.com:8443", "test")
	if err != nil {
		t.Fatalf("normalizeDomain: %v", err)
	}
	if got != "vm.example.com" {
		t.Errorf("normalizeDomain = %q, want the bare hostname vm.example.com", got)
	}

	path := writeMachineFile(t, MachineFile{
		MachineID: "mf-machine",
		AccountID: "mf-account",
		PublicURL: "https://vm.example.com:8443",
	})
	cfg, err := Resolve(Options{MachineFile: path}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.HTTPSAddr != DefaultHTTPSAddr {
		t.Errorf("HTTPSAddr = %q, want %q: the port in publicUrl is not carried into the listener", cfg.HTTPSAddr, DefaultHTTPSAddr)
	}
	// A bare host:port stays fatal, so the only way a port reaches the gateway
	// is through a scheme-bearing origin the control plane will not store.
	if _, err := normalizeDomain("vm.example.com:8443", "test"); err == nil {
		t.Error("normalizeDomain(\"vm.example.com:8443\") = nil error, want a bare host:port rejected")
	}
}

func TestResolve_UnusableDomainIsRejected(t *testing.T) {
	for _, domain := range []string{
		"https://",
		"vm.example.com:443",
		"vm.example.com/path",
	} {
		t.Run(domain, func(t *testing.T) {
			path := writeMachineFile(t, MachineFile{
				MachineID: "mf-machine",
				AccountID: "mf-account",
				PublicURL: domain,
			})
			_, err := Resolve(Options{MachineFile: path}, t.TempDir())
			if err == nil {
				t.Fatalf("expected an error: %q cannot be reduced to a hostname autocert will accept", domain)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q should name %s, the file the operator has to fix", err, path)
			}
		})
	}
}

func TestResolve_UnusableFlagDomainIsRejected(t *testing.T) {
	_, err := Resolve(Options{
		Domain:      "vm.example.com:443",
		MachineID:   "machine-1",
		AccountID:   "account-1",
		MachineFile: filepath.Join(t.TempDir(), "missing.json"),
	}, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a --domain that is not a bare hostname")
	}
	if !strings.Contains(err.Error(), "--domain") {
		t.Errorf("error %q should name the flag it came from", err)
	}
}

func TestResolve_MissingConfigurationIsClearError(t *testing.T) {
	_, err := Resolve(Options{MachineFile: filepath.Join(t.TempDir(), "missing.json")}, t.TempDir())
	if err == nil {
		t.Fatal("expected an error when nothing is configured")
	}
}

func TestResolve_InvalidDaemonAddr(t *testing.T) {
	_, err := Resolve(Options{
		Domain:      "vm.example.com",
		MachineID:   "machine-1",
		AccountID:   "account-1",
		DaemonAddr:  "not-a-host-port",
		MachineFile: filepath.Join(t.TempDir(), "missing.json"),
	}, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an invalid daemon address")
	}
}

func TestResolve_CertDirDefaultsUnderDataDir(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := Resolve(Options{
		Domain:      "vm.example.com",
		MachineID:   "machine-1",
		AccountID:   "account-1",
		MachineFile: filepath.Join(t.TempDir(), "missing.json"),
	}, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(dataDir, "vm-gateway", "certs")
	if cfg.CertDir != want {
		t.Errorf("CertDir = %q, want %q", cfg.CertDir, want)
	}
}

func TestReadMachineFile_Missing(t *testing.T) {
	mf, err := ReadMachineFile(filepath.Join(t.TempDir(), "machine.json"))
	if err != nil {
		t.Fatalf("ReadMachineFile: %v", err)
	}
	if mf != nil {
		t.Fatalf("expected nil for a missing file, got %+v", mf)
	}
}

func TestReadMachineFile_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadMachineFile(path); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

// TestDefaultMachineFilePath_IsHomeNotDataDir pins the asymmetry with CertDir
// above, which a review read as a bug: AO_DATA_DIR moves durable data, while
// machine.json is binding identity pinned to ~/.ao and moved only by
// AO_MACHINE_FILE. `ao setup-vm` writes <home>/.ao/machine.json whatever
// AO_DATA_DIR says, so deriving the read path from the data dir would point the
// gateway at a file nothing ever writes. `ao whoami` resolves through this same
// function so the two can never drift.
func TestDefaultMachineFilePath_IsHomeNotDataDir(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	got, err := DefaultMachineFilePath()
	if err != nil {
		t.Fatalf("DefaultMachineFilePath: %v", err)
	}
	if want := filepath.Join(home, ".ao", "machine.json"); got != want {
		t.Fatalf("DefaultMachineFilePath = %q, want %q", got, want)
	}

	// A data dir somewhere else entirely must not move it.
	t.Setenv("AO_DATA_DIR", t.TempDir())
	cfg, err := Resolve(Options{
		Domain: "vm.example.com", MachineID: "machine-1", AccountID: "account-1",
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Domain != "vm.example.com" {
		t.Fatalf("Domain = %q", cfg.Domain)
	}
	after, err := DefaultMachineFilePath()
	if err != nil || after != got {
		t.Fatalf("DefaultMachineFilePath = %q (err %v), want it unchanged at %q", after, err, got)
	}
}

func setHomeEnv(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		return
	}
	t.Setenv("HOME", home)
}
