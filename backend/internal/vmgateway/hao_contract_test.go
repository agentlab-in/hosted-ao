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
		Default          string `json:"default"`
		BlockedOverrides []struct {
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
	if contract.Default != "blocked" {
		t.Fatalf("default = %q, want blocked", contract.Default)
	}
	if len(contract.BlockedOverrides) != len(blockedAPIPrefixes) {
		t.Fatalf("blocked overrides = %d, runtime prefixes = %d", len(contract.BlockedOverrides), len(blockedAPIPrefixes))
	}
	for i, prefix := range blockedAPIPrefixes {
		if got := contract.BlockedOverrides[i].Path; got != prefix {
			t.Fatalf("blocked override %d = %q, runtime = %q", i, got, prefix)
		}
	}
	for _, fixture := range contract.Fixtures {
		if got := isProxyablePath(fixture.Path); got != fixture.Allowed {
			t.Errorf("isProxyablePath(%q) = %v, contract = %v", fixture.Path, got, fixture.Allowed)
		}
	}
}
