package keys

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_GeneratesDistinctActiveAndNextKeys(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if m.active.KID == m.next.KID {
		t.Fatalf("active and next keys have the same kid %q, want distinct", m.active.KID)
	}
	if len(m.active.PrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("active private key length = %d, want %d", len(m.active.PrivateKey), ed25519.PrivateKeySize)
	}
	if len(m.next.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("next public key length = %d, want %d", len(m.next.PublicKey), ed25519.PublicKeySize)
	}
}

func TestLoad_KeyFilesAreMode0600(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	for _, name := range []string{"active.json", "next.json"} {
		info, err := os.Stat(filepath.Join(dir, "keys", name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, perm)
		}
	}
}

func TestLoad_PersistsKeysAcrossReload(t *testing.T) {
	dir := t.TempDir()

	first, err := Load(dir)
	if err != nil {
		t.Fatalf("first Load() unexpected error: %v", err)
	}

	second, err := Load(dir)
	if err != nil {
		t.Fatalf("second Load() unexpected error: %v", err)
	}

	if first.active.KID != second.active.KID {
		t.Errorf("active kid changed across reload: %q != %q", first.active.KID, second.active.KID)
	}
	if first.next.KID != second.next.KID {
		t.Errorf("next kid changed across reload: %q != %q", first.next.KID, second.next.KID)
	}
}

func TestActive_SignsVerifiableMessages(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	kid, priv := m.Active()
	if kid != m.active.KID {
		t.Fatalf("Active() kid = %q, want %q", kid, m.active.KID)
	}

	msg := []byte("hello")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), msg, sig) {
		t.Fatal("signature produced by Active() private key did not verify")
	}
}

func TestRotate_PromotesNextAndGeneratesFreshNext(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	oldNextKID := m.next.KID

	if err := m.Rotate(); err != nil {
		t.Fatalf("Rotate() unexpected error: %v", err)
	}

	if m.active.KID != oldNextKID {
		t.Errorf("active kid after Rotate() = %q, want promoted next kid %q", m.active.KID, oldNextKID)
	}
	if m.next.KID == oldNextKID {
		t.Error("next kid did not change after Rotate()")
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("reload after Rotate() unexpected error: %v", err)
	}
	if reloaded.active.KID != m.active.KID {
		t.Errorf("rotation did not persist: reloaded active kid = %q, want %q", reloaded.active.KID, m.active.KID)
	}
}

func TestJWKS_PublishesActiveAndNextPublicKeysOnly(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	doc := m.JWKS()
	if len(doc.Keys) != 2 {
		t.Fatalf("len(JWKS().Keys) = %d, want 2", len(doc.Keys))
	}

	kids := map[string]jwk{doc.Keys[0].Kid: doc.Keys[0], doc.Keys[1].Kid: doc.Keys[1]}
	for _, want := range []keyPair{m.active, m.next} {
		got, ok := kids[want.KID]
		if !ok {
			t.Fatalf("JWKS missing kid %q", want.KID)
		}
		if got.Kty != "OKP" || got.Crv != "Ed25519" || got.Alg != "EdDSA" || got.Use != "sig" {
			t.Errorf("jwk %q has unexpected shape: %+v", want.KID, got)
		}
		x, err := base64.RawURLEncoding.DecodeString(got.X)
		if err != nil {
			t.Fatalf("jwk %q x is not valid base64url: %v", want.KID, err)
		}
		if string(x) != string(want.PublicKey) {
			t.Errorf("jwk %q x does not decode to the stored public key", want.KID)
		}
	}
}

func TestRegister_ServesJWKS(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", cc)
	}

	var doc jwksDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if len(doc.Keys) != 2 {
		t.Fatalf("response has %d keys, want 2", len(doc.Keys))
	}
}
