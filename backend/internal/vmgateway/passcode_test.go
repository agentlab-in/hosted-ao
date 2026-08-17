package vmgateway

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// --- passcode store: generation, persistence, corruption ---

func TestGeneratePasscode_ReturnsPlaintextAndPersistsOnlyHash(t *testing.T) {
	dir := t.TempDir()
	plaintext, err := GeneratePasscode(dir)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	if len(plaintext) != 8 {
		t.Fatalf("plaintext passcode %q has length %d, want 8", plaintext, len(plaintext))
	}

	raw, err := os.ReadFile(filepath.Join(dir, passcodeFileName))
	if err != nil {
		t.Fatalf("read persisted passcode file: %v", err)
	}
	persisted := strings.TrimSpace(string(raw))
	if persisted == plaintext {
		t.Fatalf("passcode file contains the plaintext passcode, want only the hash")
	}
	if persisted != mobilebridge.HashPassword(plaintext) {
		t.Fatalf("persisted hash = %q, want mobilebridge.HashPassword(%q) = %q", persisted, plaintext, mobilebridge.HashPassword(plaintext))
	}
}

func TestGeneratePasscode_TwoCallsProduceDifferentPasscodes(t *testing.T) {
	a, err := GeneratePasscode(t.TempDir())
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	b, err := GeneratePasscode(t.TempDir())
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	if a == b {
		t.Fatalf("two independent GeneratePasscode calls both returned %q; want a cryptographically random passcode each time", a)
	}
}

func TestLoadPasscodeStore_MissingStoreFailsLoudly(t *testing.T) {
	_, err := LoadPasscodeStore(t.TempDir())
	if err == nil {
		t.Fatal("LoadPasscodeStore on an empty directory: want an error, got nil (a gateway that starts with no usable passcode would be an open door)")
	}
}

func TestLoadPasscodeStore_CorruptStoreFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, passcodeFileName), []byte("not-a-real-hash"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	_, err := LoadPasscodeStore(dir)
	if err == nil {
		t.Fatal("LoadPasscodeStore on a corrupt file: want an error, got nil")
	}
}

func TestLoadPasscodeStore_RoundTripsWithGeneratePasscode(t *testing.T) {
	dir := t.TempDir()
	plaintext, err := GeneratePasscode(dir)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	store, err := LoadPasscodeStore(dir)
	if err != nil {
		t.Fatalf("LoadPasscodeStore after GeneratePasscode: %v", err)
	}
	if !mobilebridge.PasswordMatches(store.currentHash(), plaintext) {
		t.Fatal("loaded store does not match the passcode GeneratePasscode just persisted")
	}
}

func TestPasscodeStore_Rotate_ChangesHashAndOldPasscodeStopsMatching(t *testing.T) {
	dir := t.TempDir()
	oldPlaintext, err := GeneratePasscode(dir)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	store, err := LoadPasscodeStore(dir)
	if err != nil {
		t.Fatalf("LoadPasscodeStore: %v", err)
	}

	newPlaintext, err := store.Rotate()
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newPlaintext == oldPlaintext {
		t.Fatal("Rotate returned the same plaintext passcode as before")
	}
	if mobilebridge.PasswordMatches(store.currentHash(), oldPlaintext) {
		t.Fatal("store still matches the pre-rotation passcode; rotation must drop it")
	}
	if !mobilebridge.PasswordMatches(store.currentHash(), newPlaintext) {
		t.Fatal("store does not match the freshly rotated passcode")
	}

	// The rotation must also be durable: a fresh load from disk sees the new
	// hash, not the one GeneratePasscode originally wrote.
	reloaded, err := LoadPasscodeStore(dir)
	if err != nil {
		t.Fatalf("LoadPasscodeStore after Rotate: %v", err)
	}
	if !mobilebridge.PasswordMatches(reloaded.currentHash(), newPlaintext) {
		t.Fatal("reloaded store from disk does not match the rotated passcode")
	}
}

// --- pair-mode request verification, via NewHandler(ModePair, ...) ---

// newTestPairGateway wires a fake daemon behind a pair-mode gateway handler,
// mirroring newTestGateway in proxy_test.go but for ModePair: passcode
// verification instead of JWT.
type testPairGateway struct {
	daemonCalls []*http.Request
	daemon      *httptest.Server
	handler     http.Handler
	passcode    string
}

func newTestPairGateway(t *testing.T) *testPairGateway {
	t.Helper()
	tg := &testPairGateway{}

	tg.daemon = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		tg.daemonCalls = append(tg.daemonCalls, clone)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(tg.daemon.Close)

	daemonURL, err := url.Parse(tg.daemon.URL)
	if err != nil {
		t.Fatalf("parse daemon url: %v", err)
	}

	dir := t.TempDir()
	plaintext, err := GeneratePasscode(dir)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	tg.passcode = plaintext
	store, err := LoadPasscodeStore(dir)
	if err != nil {
		t.Fatalf("LoadPasscodeStore: %v", err)
	}

	h, err := NewPairHandler(daemonURL.Host, nil, store, []string{testOrigin}, discardLogger())
	if err != nil {
		t.Fatalf("NewPairHandler: %v", err)
	}
	tg.handler = h
	return tg
}

func (tg *testPairGateway) request(method, path, bearer, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	return rec
}

func TestPairGateway_CorrectPasscode_Authenticates(t *testing.T) {
	tg := newTestPairGateway(t)
	rec := tg.request(http.MethodGet, "/api/v1/projects", tg.passcode, "203.0.113.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if len(tg.daemonCalls) != 1 {
		t.Fatalf("daemon calls = %d, want 1", len(tg.daemonCalls))
	}
}

func TestPairGateway_WrongPasscode_Gets401WithAPIErrorEnvelope(t *testing.T) {
	tg := newTestPairGateway(t)
	rec := tg.request(http.MethodGet, "/api/v1/projects", "totally-wrong", "203.0.113.1:1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"code\"") || !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
		t.Errorf("body = %q, want the gateway's existing API error envelope with an UNAUTHORIZED code", rec.Body.String())
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("daemon calls = %d, want 0: an unauthenticated request must never reach the daemon", len(tg.daemonCalls))
	}
}

// TestPairGateway_Lockout_IsPerSource is the security property that matters
// most here: repeated failures from one source must never lock out another.
func TestPairGateway_Lockout_IsPerSource(t *testing.T) {
	original := passcodeLockoutLimit
	passcodeLockoutLimit = 3
	t.Cleanup(func() { passcodeLockoutLimit = original })

	tg := newTestPairGateway(t)
	const sourceA = "198.51.100.1:5555"
	const sourceB = "198.51.100.2:6666"

	// Drive source A past the lockout limit with wrong passcodes.
	for i := 0; i < passcodeLockoutLimit; i++ {
		rec := tg.request(http.MethodGet, "/api/v1/projects", "wrong-passcode", sourceA)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("source A attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	// Source A is now locked out, even with the CORRECT passcode.
	recA := tg.request(http.MethodGet, "/api/v1/projects", tg.passcode, sourceA)
	if recA.Code != http.StatusTooManyRequests {
		t.Fatalf("source A after %d failures, status = %d, want 429 (locked out)", passcodeLockoutLimit, recA.Code)
	}

	// Source B, presenting the correct passcode, must be entirely unaffected
	// by source A's lockout.
	recB := tg.request(http.MethodGet, "/api/v1/projects", tg.passcode, sourceB)
	if recB.Code != http.StatusOK {
		t.Fatalf("source B status = %d, want 200: source A's lockout must not affect source B, body: %s", recB.Code, recB.Body.String())
	}
}

func TestPairGateway_Lockout_ResetsAfterSuccessfulAuth(t *testing.T) {
	original := passcodeLockoutLimit
	passcodeLockoutLimit = 3
	t.Cleanup(func() { passcodeLockoutLimit = original })

	tg := newTestPairGateway(t)
	const source = "198.51.100.9:7777"

	// One fewer failure than the limit, then a success: the counter must
	// reset rather than carrying the near-miss forward.
	for i := 0; i < passcodeLockoutLimit-1; i++ {
		rec := tg.request(http.MethodGet, "/api/v1/projects", "wrong-passcode", source)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	if rec := tg.request(http.MethodGet, "/api/v1/projects", tg.passcode, source); rec.Code != http.StatusOK {
		t.Fatalf("successful auth status = %d, want 200", rec.Code)
	}

	// Repeat the same near-miss pattern immediately after: if the reset did
	// not happen, this second round would tip the source over the limit.
	for i := 0; i < passcodeLockoutLimit-1; i++ {
		rec := tg.request(http.MethodGet, "/api/v1/projects", "wrong-passcode", source)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	if rec := tg.request(http.MethodGet, "/api/v1/projects", tg.passcode, source); rec.Code != http.StatusOK {
		t.Fatalf("post-reset successful auth status = %d, want 200 (lockout must have reset after the earlier success)", rec.Code)
	}
}

func TestPairGateway_ControlRoutesStayBlocked_EvenWithValidPasscode(t *testing.T) {
	tg := newTestPairGateway(t)
	for _, path := range []string{"/shutdown", "/api/v1/mobile/status", "/api/v1/dev/reset"} {
		t.Run(path, func(t *testing.T) {
			rec := tg.request(http.MethodGet, path, tg.passcode, "203.0.113.5:1")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (blocked before proxying, regardless of a valid passcode)", rec.Code)
			}
			if len(tg.daemonCalls) != 0 {
				t.Errorf("daemon calls = %d, want 0: a control route must never reach the daemon", len(tg.daemonCalls))
			}
		})
	}
}

func TestPairGateway_RejectsAValidJWT_ThereIsNoJWKSToVerifyAgainst(t *testing.T) {
	tg := newTestPairGateway(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	jwt := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	rec := tg.request(http.MethodGet, "/api/v1/projects", jwt, "203.0.113.9:1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: pair mode has no JWKS and must not accept a JWT", rec.Code)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("daemon calls = %d, want 0", len(tg.daemonCalls))
	}
}

func TestPairGateway_CORSPreflight_NoCredentialRequired(t *testing.T) {
	tg := newTestPairGateway(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()

	tg.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if len(tg.daemonCalls) != 0 {
		t.Errorf("preflight must never reach the daemon, got %d calls", len(tg.daemonCalls))
	}
}

// --- hosted mode is unaffected by pair mode's existence ---

func TestHostedGateway_StillRejectsAPasscodeAndAcceptsAValidJWT(t *testing.T) {
	tg := newTestGateway(t)

	// A plausible 8-char alphanumeric passcode is not JWT-shaped, so it is
	// rejected as a malformed token, same as before pair mode existed.
	rec := tg.request(http.MethodGet, "/api/v1/projects", "aB3xK9mQ")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("hosted mode with a passcode-shaped bearer token: status = %d, want 401", rec.Code)
	}

	recValid := tg.request(http.MethodGet, "/api/v1/projects", tg.validToken(t))
	if recValid.Code != http.StatusOK {
		t.Fatalf("hosted mode with a valid JWT: status = %d, want 200, body: %s", recValid.Code, recValid.Body.String())
	}
}

// request is a small helper added alongside the pair-mode tests above; it
// mirrors the inline httptest.NewRequest + Authorization header pattern the
// existing hosted-mode tests in this file (proxy_test.go) already use
// individually, factored out only because the hosted/pair comparison test
// above needs it twice.
func (tg *testGateway) request(method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	return rec
}
