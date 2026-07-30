package keys

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// goldenJWKSPath is the JWKS fixture the VM gateway's tests verify real
// control-plane tokens against. It lives in the other module because that is
// where it is consumed; it is generated here, by
// controlplane/internal/tokens/golden_test.go, from keys.Manager.JWKS().
const goldenJWKSPath = "../../../backend/internal/vmgateway/testdata/jwks.json"

const regenerate = "cd controlplane && go test ./internal/tokens/ -run TestGoldenFixtures -update"

// jwkFields is the field set this package publishes per key. Named here so
// adding, removing, or renaming one is a visible edit to this list and a
// deliberate regeneration of the fixture, rather than something the gateway
// discovers on a real VM.
var jwkFields = []string{"alg", "crv", "kid", "kty", "use", "x"}

func fieldNames(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func jwksEntries(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	if got := fieldNames(t, raw); len(got) != 1 || got[0] != "keys" {
		t.Fatalf("JWKS document fields = %v, want exactly [keys]", got)
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal jwks: %v", err)
	}
	return doc.Keys
}

// TestGoldenJWKS_MirrorsFixture asserts the committed JWKS fixture is still
// the document this package produces: same field set, same constant values,
// same kid derivation. It is the control plane's half of the cross-module
// contract test, so that regenerating the fixture is a deliberate act and not
// something that quietly happens the next time somebody runs -update.
func TestGoldenJWKS_MirrorsFixture(t *testing.T) {
	raw, err := os.ReadFile(goldenJWKSPath)
	if err != nil {
		t.Fatalf("read golden JWKS: %v\nGenerate it with:\n  %s", err, regenerate)
	}

	entries := jwksEntries(t, raw)
	if len(entries) != 2 {
		t.Fatalf("golden JWKS has %d keys, want 2 (active plus next)", len(entries))
	}
	for i, entry := range entries {
		var k jwk
		if err := json.Unmarshal(entry, &k); err != nil {
			t.Fatalf("key %d: unmarshal: %v", i, err)
		}
		if got := fieldNames(t, entry); !slices.Equal(got, jwkFields) {
			t.Fatalf("key %d: fields = %v, want %v\nIf the published JWK shape changed on purpose, update jwkFields and regenerate:\n  %s", i, got, jwkFields, regenerate)
		}
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Use != "sig" || k.Alg != "EdDSA" {
			t.Fatalf("key %d = %+v, want kty OKP, crv Ed25519, use sig, alg EdDSA", i, k)
		}
		pub, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			t.Fatalf("key %d: x is not unpadded base64url: %v", i, err)
		}
		if len(pub) != 32 {
			t.Fatalf("key %d: x decodes to %d bytes, want 32", i, len(pub))
		}
		// The one assertion that catches a kid derivation change: the fixture
		// generator recomputes kid from the public key with its own copy of
		// the rule, so only this comparison against the real kidFor keeps the
		// two honest.
		if want := kidFor(pub); k.Kid != want {
			t.Fatalf("key %d: kid = %q, want kidFor(x) = %q\nThe kid derivation changed; regenerate the fixtures so the gateway sees it:\n  %s", i, k.Kid, want, regenerate)
		}
	}

	// And the live document, from freshly generated keys, still serializes to
	// the same shape as the fixture.
	active, err := generate()
	if err != nil {
		t.Fatalf("generate active: %v", err)
	}
	next, err := generate()
	if err != nil {
		t.Fatalf("generate next: %v", err)
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode((&Manager{active: active, next: next}).JWKS()); err != nil {
		t.Fatalf("encode live jwks: %v", err)
	}
	live := jwksEntries(t, buf.Bytes())
	if len(live) != len(entries) {
		t.Fatalf("live JWKS has %d keys, fixture has %d", len(live), len(entries))
	}
	for i := range live {
		if got, want := fieldNames(t, live[i]), fieldNames(t, entries[i]); !slices.Equal(got, want) {
			t.Fatalf("key %d: live fields %v, fixture fields %v; regenerate:\n  %s", i, got, want, regenerate)
		}
	}
}
