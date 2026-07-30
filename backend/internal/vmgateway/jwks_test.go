package vmgateway

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func jwksBody(t *testing.T, kid string, pub ed25519.PublicKey) []byte {
	t.Helper()
	doc := jwkDocument{Keys: []jwk{{
		Kty: "OKP",
		Crv: "Ed25519",
		X:   base64.RawURLEncoding.EncodeToString(pub),
		Kid: kid,
	}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return data
}

func TestParseKeySet_Valid(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	ks, err := parseKeySet(jwksBody(t, testKid, pub))
	if err != nil {
		t.Fatalf("parseKeySet: %v", err)
	}
	if len(ks.candidates(testKid)) != 1 {
		t.Fatalf("expected exactly one candidate for a known kid")
	}
}

func TestParseKeySet_SkipsUnsupportedKty(t *testing.T) {
	doc := jwkDocument{Keys: []jwk{{Kty: "RSA", Crv: "", X: "irrelevant", Kid: "rsa-key"}}}
	data, _ := json.Marshal(doc)
	_, err := parseKeySet(data)
	if err == nil {
		t.Fatal("expected an error: no usable keys after skipping the RSA entry")
	}
}

func TestParseKeySet_MalformedJSON(t *testing.T) {
	if _, err := parseKeySet([]byte("{not json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestJWKSCache_FetchesAndCaches(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	var fetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetches, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody(t, testKid, pub))
	}))
	defer srv.Close()

	now := time.Now()
	cache := NewJWKSCache(srv.URL, srv.Client())
	cache.now = func() time.Time { return now }

	ks, err := cache.Get(t.Context())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ks.candidates(testKid)) != 1 {
		t.Fatal("expected the fetched key to be usable")
	}
	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Errorf("fetches = %d, want 1 (second Get within TTL must not refetch)", got)
	}
}

func TestJWKSCache_RefetchesAfterTTL(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	var fetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetches, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody(t, testKid, pub))
	}))
	defer srv.Close()

	now := time.Now()
	cache := NewJWKSCache(srv.URL, srv.Client())
	cache.now = func() time.Time { return now }

	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	now = now.Add(time.Hour + time.Second)
	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("Get after TTL: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("fetches = %d, want 2 (TTL expiry must trigger a refetch)", got)
	}
}

func TestJWKSCache_StaleIfError(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	healthy := int32(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&healthy) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody(t, testKid, pub))
	}))
	defer srv.Close()

	now := time.Now()
	cache := NewJWKSCache(srv.URL, srv.Client())
	cache.now = func() time.Time { return now }

	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("initial Get: %v", err)
	}

	atomic.StoreInt32(&healthy, 0)
	now = now.Add(time.Hour + time.Second)
	ks, err := cache.Get(t.Context())
	if err != nil {
		t.Fatalf("Get during outage: %v, want stale-if-error to suppress the failure", err)
	}
	if len(ks.candidates(testKid)) != 1 {
		t.Fatal("expected the stale keyset to still be usable")
	}
}

func TestJWKSCache_FailsClosedWithoutEverSucceeding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cache := NewJWKSCache(srv.URL, srv.Client())
	if _, err := cache.Get(t.Context()); err == nil {
		t.Fatal("expected an error: no cached keyset exists and the fetch failed, so verification must fail closed")
	}
}
