package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

func TestWhoamiReportsTheBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine.json")
	content, err := renderMachineFile(vmgateway.MachineFile{
		MachineID: testMachineID, AccountID: testAccountID, PublicURL: testPublicURL,
	}, time.Date(2026, 7, 30, 9, 15, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMachineFile(path, content, nil); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{}, "whoami", "--machine-file", path)
	if err != nil {
		t.Fatalf("ao whoami: %v", err)
	}
	for _, want := range []string{
		"bound to an AO account",
		testMachineID,
		testAccountID,
		testPublicURL,
		"2026-07-30T09:15:30Z",
		path,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ao whoami output is missing %q:\n%s", want, out)
		}
	}
	assertNoDashes(t, out)
}

func TestWhoamiOnAnUnboundMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine.json")

	out, _, err := executeCLI(t, Deps{}, "whoami", "--machine-file", path)
	// A missing machine file is the normal not-bound-yet state, the same way
	// the gateway's reader treats it, so it is reported rather than raised.
	if err != nil {
		t.Fatalf("a missing machine file must not be an error: %v", err)
	}
	for _, want := range []string{"not bound to an AO account", path, "ao setup-vm --domain"} {
		if !strings.Contains(out, want) {
			t.Errorf("ao whoami output is missing %q:\n%s", want, out)
		}
	}
	assertNoDashes(t, out)
}

// TestWhoamiFlagsAFileTheGatewayCannotUse catches the binding that looks fine
// until `ao vm serve` starts: a publicUrl that is a bare hostname leaves the
// certificate whitelist empty, and a machine.json is fatal to the gateway
// rather than ignored.
func TestWhoamiFlagsAFileTheGatewayCannotUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine.json")
	if err := os.WriteFile(path, []byte(`{"machineId":"`+testMachineID+
		`","accountId":"`+testAccountID+`","publicUrl":"vm.example.com","issuedAt":"2026-07-30T09:15:30Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{}, "whoami", "--machine-file", path)
	if err != nil {
		t.Fatalf("ao whoami: %v", err)
	}
	if !strings.Contains(out, "not usable") || !strings.Contains(out, "full origin") {
		t.Errorf("an unusable machine file must be reported as such:\n%s", out)
	}
	assertNoDashes(t, out)
}

func TestResolveMachineFilePathPrecedence(t *testing.T) {
	t.Setenv("AO_MACHINE_FILE", "/from/env/machine.json")

	got, err := resolveMachineFilePath("/from/flag/machine.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/from/flag/machine.json" {
		t.Errorf("path = %q, want the flag to win", got)
	}

	got, err = resolveMachineFilePath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/from/env/machine.json" {
		t.Errorf("path = %q, want AO_MACHINE_FILE", got)
	}

	// The default is $HOME/.ao/machine.json, not the AO_DATA_DIR-resolved data
	// dir, because that is where vmgateway's reader looks. The two have to name
	// the same file or whoami would report a binding the gateway never sees.
	t.Setenv("AO_MACHINE_FILE", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	got, err = resolveMachineFilePath("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".ao", "machine.json"); got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
}
