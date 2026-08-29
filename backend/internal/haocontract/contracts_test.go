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

	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
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

func TestEmbeddedConfigSchemaMatchesPublishedContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(contractDir(t), "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != haocontract.ConfigSchemaJSON {
		t.Fatal("embedded hao config schema is stale; run go generate ./internal/haocontract")
	}
}

func TestJSONContractsAreWellFormedAndComplete(t *testing.T) {
	dir := contractDir(t)
	for _, name := range []string{
		"config.schema.json", "errors.json", "compatibility.json",
		"prerequisites.json", "gateway-route-policy.json", "legacy/manifest.json",
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
	envelopeCodes := map[string]bool{}
	var rawErrors struct {
		ErrorEnvelope struct {
			Properties struct {
				Code struct {
					Enum []string `json:"enum"`
				} `json:"code"`
			} `json:"properties"`
		} `json:"errorEnvelope"`
	}
	decodeJSON(t, filepath.Join(dir, "errors.json"), &rawErrors)
	for _, code := range rawErrors.ErrorEnvelope.Properties.Code.Enum {
		envelopeCodes[code] = true
	}
	if !reflect.DeepEqual(envelopeCodes, seenCodes) {
		t.Fatalf("envelope code enum = %v, taxonomy = %v", envelopeCodes, seenCodes)
	}
}

func TestErrorEnvelopeExamplesMatchPublishedSchema(t *testing.T) {
	dir := contractDir(t)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("errors.json", mustOpen(t, filepath.Join(dir, "errors.json"))); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("errors.json#/errorEnvelope")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"valid.json", true},
		{"invalid-code.json", false},
		{"invalid-empty-framing.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			decodeJSON(t, filepath.Join(dir, "examples", "errors", tc.name), &value)
			err := schema.Validate(value)
			if tc.valid && err != nil {
				t.Fatalf("valid envelope rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
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
			ID           string `json:"id"`
			Consumer     string `json:"consumer"`
			Provider     string `json:"provider"`
			Negotiation  string `json:"negotiation"`
			Versions     []int  `json:"versions"`
			WireContract struct {
				Endpoint       string `json:"endpoint"`
				ResponseFields []struct {
					Name     string `json:"name"`
					Type     string `json:"type"`
					Required bool   `json:"required"`
				} `json:"responseFields"`
				CheckFields []struct {
					Name     string `json:"name"`
					Type     string `json:"type"`
					Required bool   `json:"required"`
				} `json:"checkFields"`
				CheckIDs string `json:"checkIDs"`
			} `json:"wireContract"`
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
	byInterface := map[string]struct {
		Negotiation string
		Versions    []int
		Endpoint    string
		CheckIDs    string
	}{}
	for _, iface := range compatibility.Interfaces {
		if iface.ID == "" || iface.Consumer == "" || iface.Provider == "" || iface.Negotiation == "" || seen[iface.ID] {
			t.Fatalf("invalid compatibility interface: %+v", iface)
		}
		if iface.ID == "ao-daemon-api" {
			if len(iface.Versions) != 0 {
				t.Fatalf("unversioned daemon doctor wire must not advertise versions: %+v", iface)
			}
		} else if !reflect.DeepEqual(iface.Versions, []int{1}) {
			t.Fatalf("versioned interface must support exactly v1: %+v", iface)
		}
		seen[iface.ID] = true
		byInterface[iface.ID] = struct {
			Negotiation string
			Versions    []int
			Endpoint    string
			CheckIDs    string
		}{iface.Negotiation, iface.Versions, iface.WireContract.Endpoint, iface.WireContract.CheckIDs}
	}
	for _, required := range []string{"hao-config", "ao-daemon-api", "daemon-gateway-api", "gateway-desktop-transport", "ao-pair", "gateway-route-policy", "legacy-installation-shape"} {
		if !seen[required] {
			t.Errorf("missing compatibility interface %q", required)
		}
	}
	daemon := byInterface["ao-daemon-api"]
	if daemon.Negotiation != "compatibility-pinned-current-wire" || daemon.Endpoint != "/api/v1/doctor" || daemon.CheckIDs != "daemon-defined-unversioned" {
		t.Fatalf("daemon negotiation contract is incomplete: %+v", daemon)
	}
	readme, err := os.ReadFile(filepath.Join(contractDir(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for id := range seen {
		if !strings.Contains(string(readme), "`"+id+"`") {
			t.Errorf("compatibility table is missing interface %q", id)
		}
	}
}

func TestPrerequisiteOwnershipAndProfiles(t *testing.T) {
	var contract struct {
		DefaultWorkflowProfile string `json:"defaultWorkflowProfile"`
		WorkflowProfiles       map[string]struct {
			Requires []string `json:"requires"`
		} `json:"workflowProfiles"`
		Prerequisites []struct {
			ID            string `json:"id"`
			Owner         string `json:"owner"`
			Condition     string `json:"condition"`
			InstallPolicy string `json:"installPolicy"`
		} `json:"prerequisites"`
		ForbiddenMachinePolicyKeys []string `json:"forbiddenMachinePolicyKeys"`
	}
	decodeJSON(t, filepath.Join(contractDir(t), "prerequisites.json"), &contract)
	if contract.DefaultWorkflowProfile != "general" {
		t.Fatalf("default workflow profile = %q", contract.DefaultWorkflowProfile)
	}
	if !reflect.DeepEqual(contract.WorkflowProfiles["general"].Requires, []string{"git", "harness"}) ||
		!reflect.DeepEqual(contract.WorkflowProfiles["github"].Requires, []string{"git", "harness", "gh"}) {
		t.Fatalf("workflow prerequisite profiles = %+v", contract.WorkflowProfiles)
	}
	byID := map[string]struct{ Owner, Condition, InstallPolicy string }{}
	validInstallPolicies := map[string]bool{"always": true, "allowlisted-only": true, "never": true}
	for _, prerequisite := range contract.Prerequisites {
		if prerequisite.ID == "" || prerequisite.Owner == "" || prerequisite.Condition == "" || !validInstallPolicies[prerequisite.InstallPolicy] {
			t.Fatalf("invalid prerequisite contract: %+v", prerequisite)
		}
		if _, exists := byID[prerequisite.ID]; exists {
			t.Fatalf("duplicate prerequisite id %q", prerequisite.ID)
		}
		byID[prerequisite.ID] = struct{ Owner, Condition, InstallPolicy string }{prerequisite.Owner, prerequisite.Condition, prerequisite.InstallPolicy}
	}
	if byID["ao-runtime"].Owner != "ao-artifact" || byID["ao-runtime"].InstallPolicy != "never" ||
		byID["gh"].Condition != "workflow-profile:github" || byID["harness"].InstallPolicy != "allowlisted-only" {
		t.Fatalf("prerequisite ownership/conditions = %+v", byID)
	}
	for profile, definition := range contract.WorkflowProfiles {
		for _, required := range definition.Requires {
			if _, exists := byID[required]; !exists {
				t.Errorf("workflow profile %q references unknown prerequisite %q", profile, required)
			}
		}
	}
	if !reflect.DeepEqual(contract.ForbiddenMachinePolicyKeys, []string{"runtimeBackend", "tmux"}) {
		t.Fatalf("forbidden machine policy keys = %v", contract.ForbiddenMachinePolicyKeys)
	}
	var schema struct {
		Properties struct {
			Workflow struct {
				Properties struct {
					Profile struct {
						Enum []string `json:"enum"`
					} `json:"profile"`
				} `json:"properties"`
			} `json:"workflow"`
		} `json:"properties"`
	}
	schemaPath := filepath.Join(contractDir(t), "config.schema.json")
	decodeJSON(t, schemaPath, &schema)
	profiles := make([]string, 0, len(contract.WorkflowProfiles))
	for profile := range contract.WorkflowProfiles {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	sort.Strings(schema.Properties.Workflow.Properties.Profile.Enum)
	if !reflect.DeepEqual(profiles, schema.Properties.Workflow.Properties.Profile.Enum) {
		t.Fatalf("schema workflow profiles = %v, prerequisite profiles = %v", schema.Properties.Workflow.Properties.Profile.Enum, profiles)
	}
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range contract.ForbiddenMachinePolicyKeys {
		if strings.Contains(string(schemaBytes), `"`+forbidden+`"`) {
			t.Errorf("config schema contains forbidden machine policy key %q", forbidden)
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
