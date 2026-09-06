package vmgateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

// Runtime isolation must not select a fresh gateway identity and invalidate
// existing certificate fingerprint pins. Provisioning owns these defaults.
func TestRuntimeOverridesPreserveGatewayIdentity(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)
	root, err := config.DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	machine := filepath.Join(root, "machine.json")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"machineId":"stable-machine","accountId":"stable-account","publicUrl":"stable.example.com"}`)
	if err := os.WriteFile(machine, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, data, run string }{
		{name: "default"},
		{name: "data", data: filepath.Join(home, "runtime", "data")},
		{name: "run", run: filepath.Join(home, "discovery", "running.json")},
		{name: "both", data: filepath.Join(home, "runtime", "data"), run: filepath.Join(home, "discovery", "running.json")},
		{name: "relative data", data: filepath.Join("relative-runtime", "data")},
		{name: "relative run", run: filepath.Join("relative-discovery", "running.json")},
		{name: "relative both", data: filepath.Join("relative-runtime", "data"), run: filepath.Join("relative-discovery", "running.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", tc.data)
			t.Setenv("AO_RUN_FILE", tc.run)
			for _, key := range []string{"AO_MACHINE_FILE", "AO_VM_CERT_DIR", "AO_VM_PASSCODE_DIR", "AO_VM_PAIR", "AO_VM_DOMAIN", "AO_VM_MACHINE_ID", "AO_VM_ACCOUNT_ID", "AO_VM_ISSUER", "AO_VM_JWKS_URL", "AO_VM_HTTP_ADDR"} {
				t.Setenv(key, "")
			}
			daemon, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			got, err := DefaultMachineFilePath()
			if err != nil || got != machine {
				t.Fatalf("machine path=%q err=%v", got, err)
			}
			hosted, err := Resolve(Options{}, daemon.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if hosted.MachineID != "stable-machine" {
				t.Fatalf("selected different identity: %q", hosted.MachineID)
			}
			pair, err := Resolve(Options{Pair: true}, daemon.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if pair.MachineID != "stable-machine" || pair.CertDir != filepath.Join(root, "vm-gateway", "pair-cert") || pair.PasscodeDir != filepath.Join(root, "vm-gateway", "pair-passcode") {
				t.Fatalf("pair identity moved: %+v", pair)
			}
			override := writeMachineFile(t, MachineFile{MachineID: "explicit-machine", AccountID: "explicit-account", PublicURL: "explicit.example.com"})
			cert := filepath.Join(home, "explicit-cert")
			passcode := filepath.Join(home, "explicit-passcode")
			t.Setenv("AO_MACHINE_FILE", override)
			t.Setenv("AO_VM_CERT_DIR", cert)
			t.Setenv("AO_VM_PASSCODE_DIR", passcode)
			pair, err = Resolve(Options{Pair: true}, daemon.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if pair.MachineID != "explicit-machine" || pair.CertDir != cert || pair.PasscodeDir != passcode {
				t.Fatalf("explicit identity overrides lost: %+v", pair)
			}
			flagCert := filepath.Join(home, "flag-cert")
			flagPass := filepath.Join(home, "flag-passcode")
			pair, err = Resolve(Options{Pair: true, MachineFile: machine, CertDir: flagCert, PasscodeDir: flagPass}, daemon.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if pair.MachineID != "stable-machine" || pair.CertDir != flagCert || pair.PasscodeDir != flagPass {
				t.Fatalf("flag precedence lost: %+v", pair)
			}
		})
	}
	actual, err := os.ReadFile(machine)
	if err != nil || string(actual) != string(original) {
		t.Fatalf("identity rewritten: err=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "machine.json" {
		t.Fatalf("resolution generated identity files: %v", entries)
	}
}
