package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

func TestHAOLegacyServiceFixturesMatchCurrentSetupVM(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "legacy", "service-shapes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]struct {
		UnitName    string            `json:"unitName"`
		After       []string          `json:"after"`
		Environment map[string]string `json:"environment"`
		ExecStart   string            `json:"execStart"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		unit string
	}{
		{"daemon", renderDaemonUnit(testSetupPlan(t))},
		{"hostedGateway", renderGatewayUnit(testSetupPlan(t))},
		{"pairGateway", renderGatewayUnit(testPairSetupPlan(t))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, ok := fixtures[tc.name]
			if !ok {
				t.Fatal("missing fixture")
			}
			if !strings.Contains(tc.unit, "ExecStart="+fixture.ExecStart) {
				t.Errorf("unit does not contain fixture entrypoint %q", fixture.ExecStart)
			}
			for key, value := range fixture.Environment {
				if !strings.Contains(tc.unit, `Environment="`+key+`=`+value+`"`) {
					t.Errorf("unit does not contain fixture environment %s=%s", key, value)
				}
			}
			for _, dependency := range fixture.After {
				if !strings.Contains(tc.unit, "After=") || !strings.Contains(tc.unit, dependency) {
					t.Errorf("unit does not start after %s", dependency)
				}
			}
			if fixture.UnitName != setupVMDaemonUnit && fixture.UnitName != setupVMGatewayUnit {
				t.Errorf("fixture unit name %q is not a current setup-vm unit", fixture.UnitName)
			}
		})
	}
}

func TestHAOLegacyManifestUsesSharedStateRoot(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "legacy", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		StateRootSegments []string `json:"stateRootSegments"`
		BinaryPath        string   `json:"binaryPath"`
		UnitDirectory     string   `json:"unitDirectory"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if strings.Join(manifest.StateRootSegments, "/") != strings.Join(config.StateRootSegments(), "/") {
		t.Fatalf("fixture state root = %v, runtime = %v", manifest.StateRootSegments, config.StateRootSegments())
	}
	if manifest.BinaryPath != setupVMBinaryPath || manifest.UnitDirectory != setupVMUnitDir {
		t.Fatalf("legacy paths drifted: fixture binary=%q unitDir=%q", manifest.BinaryPath, manifest.UnitDirectory)
	}
}
