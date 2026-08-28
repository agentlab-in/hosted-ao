package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

type legacyServiceFixture struct {
	UnitName         string            `json:"unitName"`
	After            []string          `json:"after"`
	User             string            `json:"user"`
	WorkingDirectory string            `json:"workingDirectory"`
	Environment      map[string]string `json:"environment"`
	ExecStart        string            `json:"execStart"`
}

func loadLegacyServiceFixtures(t *testing.T) map[string]legacyServiceFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "legacy", "service-shapes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]legacyServiceFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func parseUnitSettings(t *testing.T, unit string) map[string][]string {
	t.Helper()
	settings := map[string][]string{}
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid rendered unit line %q", line)
		}
		settings[key] = append(settings[key], strings.Trim(value, `"`))
	}
	return settings
}

func TestHAOLegacyServiceFixturesMatchCurrentSetupVM(t *testing.T) {
	fixtures := loadLegacyServiceFixtures(t)
	cases := []struct {
		name     string
		unitName string
		unit     string
	}{
		{"daemon", setupVMDaemonUnit, renderDaemonUnit(testSetupPlan(t))},
		{"hostedGateway", setupVMGatewayUnit, renderGatewayUnit(testSetupPlan(t))},
		{"pairGateway", setupVMGatewayUnit, renderGatewayUnit(testPairSetupPlan(t))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, ok := fixtures[tc.name]
			if !ok {
				t.Fatal("missing fixture")
			}
			settings := parseUnitSettings(t, tc.unit)
			if fixture.UnitName != tc.unitName {
				t.Errorf("fixture unit name = %q, want %q", fixture.UnitName, tc.unitName)
			}
			for key, want := range map[string]string{
				"ExecStart": fixture.ExecStart, "User": fixture.User, "WorkingDirectory": fixture.WorkingDirectory,
			} {
				if want != "" && !reflect.DeepEqual(settings[key], []string{want}) {
					t.Errorf("%s = %v, fixture = %q", key, settings[key], want)
				}
			}
			environment := map[string]string{}
			for _, setting := range settings["Environment"] {
				key, value, ok := strings.Cut(setting, "=")
				if !ok {
					t.Fatalf("invalid Environment setting %q", setting)
				}
				environment[key] = value
			}
			for key, want := range fixture.Environment {
				if got := environment[key]; got != want {
					t.Errorf("environment %s = %q, fixture = %q", key, got, want)
				}
			}
			for _, dependency := range fixture.After {
				if !containsWord(settings["After"], dependency) {
					t.Errorf("After = %v, missing fixture dependency %q", settings["After"], dependency)
				}
			}
		})
	}
}

func containsWord(values []string, want string) bool {
	for _, value := range values {
		for _, word := range strings.Fields(value) {
			if word == want {
				return true
			}
		}
	}
	return false
}

func TestHAOLegacyManifestMatchesFixturesAndRuntime(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "legacy", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		StateRootSegments []string `json:"stateRootSegments"`
		BinaryPath        string   `json:"binaryPath"`
		UnitDirectory     string   `json:"unitDirectory"`
		Services          []struct {
			Shape      string   `json:"shape"`
			UnitName   string   `json:"unitName"`
			Fixture    string   `json:"fixture"`
			Entrypoint []string `json:"entrypoint"`
		} `json:"services"`
		State []struct {
			Shape   string   `json:"shape"`
			Path    string   `json:"path"`
			Fixture string   `json:"fixture"`
			Entries []string `json:"entries"`
		} `json:"state"`
		CLIEntryPoints [][]string `json:"cliEntryPoints"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.StateRootSegments, config.StateRootSegments()) {
		t.Fatalf("fixture state root = %v, runtime = %v", manifest.StateRootSegments, config.StateRootSegments())
	}
	if manifest.BinaryPath != setupVMBinaryPath || manifest.UnitDirectory != setupVMUnitDir {
		t.Fatalf("legacy paths drifted: fixture binary=%q unitDir=%q", manifest.BinaryPath, manifest.UnitDirectory)
	}
	fixtures := loadLegacyServiceFixtures(t)
	fixtureKeys := map[string]string{"daemon": "daemon", "hosted-gateway": "hostedGateway", "pair-gateway": "pairGateway"}
	if len(manifest.Services) != len(fixtureKeys) {
		t.Fatalf("manifest services = %d, want %d", len(manifest.Services), len(fixtureKeys))
	}
	seenServices := map[string]bool{}
	for _, service := range manifest.Services {
		fixture, ok := fixtures[fixtureKeys[service.Shape]]
		if !ok || seenServices[service.Shape] || service.Fixture != "service-shapes.json" || service.UnitName != fixture.UnitName ||
			strings.Join(service.Entrypoint, " ") != fixture.ExecStart {
			t.Errorf("manifest service does not match fixture: %+v", service)
		}
		seenServices[service.Shape] = true
	}
	wantState := map[string]string{
		"hosted-machine": "machine.json", "pair-certificate": "vm-gateway/pair-cert", "pair-passcode": "vm-gateway/pair-passcode",
	}
	wantEntries := map[string][]string{
		"pair-certificate": {"cert.pem", "key.pem"}, "pair-passcode": {"passcode.hash"},
	}
	if len(manifest.State) != len(wantState) {
		t.Fatalf("manifest state shapes = %d, want %d", len(manifest.State), len(wantState))
	}
	seenState := map[string]bool{}
	for _, state := range manifest.State {
		if seenState[state.Shape] || wantState[state.Shape] != state.Path || !reflect.DeepEqual(state.Entries, wantEntries[state.Shape]) {
			t.Errorf("legacy state %+v is not a recognized current path", state)
		}
		seenState[state.Shape] = true
		if state.Fixture != "" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), state.Fixture)); err != nil {
				t.Errorf("legacy state fixture %q: %v", state.Fixture, err)
			}
		}
	}
	root := NewRootCommand(Deps{})
	if len(manifest.CLIEntryPoints) != 7 {
		t.Fatalf("legacy CLI entry points = %d, want 7", len(manifest.CLIEntryPoints))
	}
	for _, entrypoint := range manifest.CLIEntryPoints {
		if len(entrypoint) < 2 || entrypoint[0] != "ao" {
			t.Errorf("invalid legacy CLI entry point %v", entrypoint)
			continue
		}
		words := entrypoint[1:]
		var flags []string
		for i, word := range words {
			if strings.HasPrefix(word, "-") {
				flags = append(flags, words[i:]...)
				words = words[:i]
				break
			}
		}
		command, _, err := root.Find(words)
		if err != nil || command == root {
			t.Errorf("legacy CLI entry point %v is not present: %v", entrypoint, err)
			continue
		}
		for _, flag := range flags {
			name := strings.TrimLeft(flag, "-")
			if command.Flags().Lookup(name) == nil {
				t.Errorf("legacy CLI entry point %v refers to missing flag %q", entrypoint, flag)
			}
		}
	}
}
