package vmgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHAORoutePolicyContractMatchesGateway(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "gateway-route-policy.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Version int    `json:"version"`
		Default string `json:"default"`
		Allowed []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"allowed"`
		BlockedOverrides []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"blockedOverrides"`
		Fixtures []struct {
			Path    string `json:"path"`
			Allowed bool   `json:"allowed"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Version != 1 || contract.Default != "blocked" {
		t.Fatalf("policy header = version %d default %q, want version 1 default blocked", contract.Version, contract.Default)
	}
	if len(contract.Allowed) != 2 || contract.Allowed[0].Kind != "exact" || contract.Allowed[0].Path != muxPath ||
		contract.Allowed[1].Kind != "prefix" || contract.Allowed[1].Path != "/api/v1" {
		t.Fatalf("allowed policy does not match runtime base allowlist: %+v", contract.Allowed)
	}
	if len(contract.BlockedOverrides) != len(blockedAPIPrefixes) {
		t.Fatalf("blocked overrides = %d, runtime prefixes = %d", len(contract.BlockedOverrides), len(blockedAPIPrefixes))
	}
	for i, prefix := range blockedAPIPrefixes {
		if got := contract.BlockedOverrides[i]; got.Kind != "prefix" || got.Path != prefix {
			t.Fatalf("blocked override %d = %+v, runtime prefix = %q", i, got, prefix)
		}
	}
	for _, fixture := range contract.Fixtures {
		if got := isProxyablePath(fixture.Path); got != fixture.Allowed {
			t.Errorf("isProxyablePath(%q) = %v, contract = %v", fixture.Path, got, fixture.Allowed)
		}
	}
	boundaryCases := map[string]bool{
		"/api/v1":                true,
		"/api/v1/":               true,
		"/api/v1evil":            false,
		"/api/v1/mobile":         false,
		"/api/v1/mobile/":        false,
		"/api/v1/mobileapp":      true,
		"/api/v1/devices":        true,
		"/api/v1/system/install": false,
		"/mux":                   true,
		"/mux/":                  false,
	}
	for path, allowed := range boundaryCases {
		if got := isProxyablePath(path); got != allowed {
			t.Errorf("segment-boundary isProxyablePath(%q) = %v, want %v", path, got, allowed)
		}
	}
}
