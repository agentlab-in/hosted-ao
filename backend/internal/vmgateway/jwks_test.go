package vmgateway

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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

// A failed refresh must not be retried per request: with the control plane
// down, every request would otherwise start its own fetch and wait out the
// client timeout, which is what stale-if-error exists to prevent.
func TestJWKSCache_StaleIfError_BacksOffInsteadOfRefetching(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	healthy := int32(1)
	var fetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetches, 1)
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
	for i := range 5 {
		if _, err := cache.Get(t.Context()); err != nil {
			t.Fatalf("Get %d during outage: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("fetches = %d, want 2 (one success, then one failure that backs off)", got)
	}

	now = now.Add(failureBackoff + time.Second)
	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("Get after the backoff window: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 3 {
		t.Errorf("fetches = %d, want 3 (the backoff window has passed, so retry once)", got)
	}
}

// The refresh must not run under the cache mutex: a slow or hanging control
// plane would otherwise queue every concurrent request behind the client
// timeout, one after another.
func TestJWKSCache_RefreshDoesNotBlockOtherCallers(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&fetches, 1) > 1 {
			entered <- struct{}{}
			<-release // model a control plane that has stopped answering
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody(t, testKid, pub))
	}))
	defer srv.Close()
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	now := time.Now()
	cache := NewJWKSCache(srv.URL, srv.Client())
	cache.now = func() time.Time { return now }

	if _, err := cache.Get(t.Context()); err != nil {
		t.Fatalf("initial Get: %v", err)
	}

	now = now.Add(time.Hour + time.Second)
	slow := make(chan struct{})
	go func() {
		defer close(slow)
		_, _ = cache.Get(context.Background())
	}()
	<-entered // the refresh is now in flight and will not return on its own

	second := make(chan *KeySet, 1)
	go func() {
		ks, err := cache.Get(context.Background())
		if err != nil {
			t.Errorf("Get while a refresh is in flight: %v", err)
		}
		second <- ks
	}()

	select {
	case ks := <-second:
		if ks == nil || len(ks.candidates(testKid)) != 1 {
			t.Fatal("expected the cached keyset while the refresh is in flight")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Get queued behind the in-flight refresh instead of serving the cached keyset")
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("fetches = %d, want 2 (the second caller must not start its own)", got)
	}

	releaseAll()
	<-slow
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
