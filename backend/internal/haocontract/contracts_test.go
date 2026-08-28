package haocontract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

func contractDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "hao", "v1"))
}

func TestConfigExamplesMatchSchema(t *testing.T) {
	dir := contractDir(t)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("config.schema.json", mustOpen(t, filepath.Join(dir, "config.schema.json"))); err != nil {
		t.Fatalf("load config schema: %v", err)
	}
	schema, err := compiler.Compile("config.schema.json")
	if err != nil {
		t.Fatalf("compile config schema: %v", err)
	}

	for _, tc := range []struct {
		pattern string
		valid   bool
	}{
		{filepath.Join(dir, "examples", "valid", "*.yaml"), true},
		{filepath.Join(dir, "examples", "invalid", "*.yaml"), false},
	} {
		paths, err := filepath.Glob(tc.pattern)
		if err != nil || len(paths) == 0 {
			t.Fatalf("glob %q: paths=%v err=%v", tc.pattern, paths, err)
		}
		for _, path := range paths {
			t.Run(filepath.Base(path), func(t *testing.T) {
				var value any
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := yaml.Unmarshal(data, &value); err != nil {
					t.Fatalf("parse YAML: %v", err)
				}
				err = schema.Validate(value)
				if tc.valid && err != nil {
					t.Fatalf("valid example rejected: %v", err)
				}
				if !tc.valid && err == nil {
					t.Fatal("invalid example accepted")
				}
			})
		}
	}
}

func TestJSONContractsAreWellFormedAndComplete(t *testing.T) {
	dir := contractDir(t)
	for _, name := range []string{
		"config.schema.json", "errors.json", "compatibility.json",
		"gateway-route-policy.json", "legacy/manifest.json",
		"legacy/machine.json", "legacy/service-shapes.json",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Errorf("parse %s: %v", name, err)
		}
	}

	var errors struct {
		ExitStatuses []struct {
			Status int `json:"status"`
		} `json:"exitStatuses"`
		Codes []struct {
			Code       string `json:"code"`
			ExitStatus int    `json:"exitStatus"`
		} `json:"codes"`
		ErrorEnvelope struct {
			Required []string `json:"required"`
		} `json:"errorEnvelope"`
	}
	decodeJSON(t, filepath.Join(dir, "errors.json"), &errors)
	statuses := make([]int, 0, len(errors.ExitStatuses))
	known := map[int]bool{}
	for _, item := range errors.ExitStatuses {
		statuses = append(statuses, item.Status)
		known[item.Status] = true
	}
	sort.Ints(statuses)
	if !reflect.DeepEqual(statuses, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("exit statuses = %v, want 0 through 4", statuses)
	}
	seenCodes := map[string]bool{}
	for _, item := range errors.Codes {
		if item.Code == "" || seenCodes[item.Code] || !known[item.ExitStatus] || item.ExitStatus == 0 {
			t.Fatalf("invalid error code entry: %+v", item)
		}
		seenCodes[item.Code] = true
	}
	wantFields := []string{"code", "message", "component", "operation", "remediation", "details"}
	if strings.Join(errors.ErrorEnvelope.Required, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("error envelope required fields = %v, want %v", errors.ErrorEnvelope.Required, wantFields)
	}
}

func TestLegacyMachineFixtureMatchesGatewayShape(t *testing.T) {
	path := filepath.Join(contractDir(t), "legacy", "machine.json")
	machine, err := vmgateway.ReadMachineFile(path)
	if err != nil {
		t.Fatalf("read legacy machine fixture: %v", err)
	}
	if machine == nil || machine.MachineID == "" || machine.AccountID == "" || machine.PublicURL == "" || machine.IssuedAt.IsZero() {
		t.Fatalf("legacy machine fixture is incomplete: %+v", machine)
	}
}

func TestCompatibilityContractNamesEveryBoundaryComponent(t *testing.T) {
	var compatibility struct {
		UnknownInterfaceVersion string   `json:"unknownInterfaceVersion"`
		Components              []string `json:"components"`
		Interfaces              []struct {
			ID       string `json:"id"`
			Versions []int  `json:"versions"`
		} `json:"interfaces"`
	}
	decodeJSON(t, filepath.Join(contractDir(t), "compatibility.json"), &compatibility)
	wantComponents := []string{"hao", "ao-daemon", "gateway", "desktop", "config-schema", "legacy-service"}
	if !reflect.DeepEqual(compatibility.Components, wantComponents) {
		t.Fatalf("components = %v, want %v", compatibility.Components, wantComponents)
	}
	if compatibility.UnknownInterfaceVersion != "unsupported" {
		t.Fatalf("unknown interface policy = %q, want unsupported", compatibility.UnknownInterfaceVersion)
	}
	seen := map[string]bool{}
	for _, iface := range compatibility.Interfaces {
		if iface.ID == "" || seen[iface.ID] || !reflect.DeepEqual(iface.Versions, []int{1}) {
			t.Fatalf("invalid compatibility interface: %+v", iface)
		}
		seen[iface.ID] = true
	}
	for _, required := range []string{"hao-config", "ao-pair", "gateway-route-policy", "legacy-installation-shape"} {
		if !seen[required] {
			t.Errorf("missing compatibility interface %q", required)
		}
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func decodeJSON(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatal(err)
	}
}
