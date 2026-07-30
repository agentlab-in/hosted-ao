package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	key, err := loadOrCreateSessionKey(t.TempDir())
	if err != nil {
		t.Fatalf("loadOrCreateSessionKey() error: %v", err)
	}
	return &Service{sessionKey: key}
}

func TestLoadOrCreateSessionKey_PersistsAcrossCalls(t *testing.T) {
	dir := t.TempDir()

	first, err := loadOrCreateSessionKey(dir)
	if err != nil {
		t.Fatalf("first loadOrCreateSessionKey() error: %v", err)
	}
	if len(first) != sessionKeyLen {
		t.Fatalf("key length = %d, want %d", len(first), sessionKeyLen)
	}

	second, err := loadOrCreateSessionKey(dir)
	if err != nil {
		t.Fatalf("second loadOrCreateSessionKey() error: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("loadOrCreateSessionKey() returned a different key on the second call, want the persisted one")
	}
}

// Creating the data dir on demand must use the same 0700 as
// storage/sqlite.Open: everything in it is sensitive, this key included.
func TestLoadOrCreateSessionKey_CreatesDataDirMode0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "controlplane-data")

	if _, err := loadOrCreateSessionKey(dir); err != nil {
		t.Fatalf("loadOrCreateSessionKey() error: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %o, want 0700", perm)
	}
}

func TestSession_IssueAndReadRoundTrip(t *testing.T) {
	s := newTestService(t)

	rec := httptest.NewRecorder()
	s.issueSession(rec, "account-123")

	req := httptest.NewRequest(http.MethodGet, "/device", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	accountID, ok := s.AccountFromRequest(req)
	if !ok {
		t.Fatal("AccountFromRequest() ok = false, want true")
	}
	if accountID != "account-123" {
		t.Errorf("accountID = %q, want %q", accountID, "account-123")
	}
}

func TestSession_NoCookieRejected(t *testing.T) {
	s := newTestService(t)
	req := httptest.NewRequest(http.MethodGet, "/device", nil)

	if _, ok := s.AccountFromRequest(req); ok {
		t.Fatal("AccountFromRequest() ok = true with no cookie, want false")
	}
}

func TestSession_TamperedCookieRejected(t *testing.T) {
	s := newTestService(t)

	rec := httptest.NewRecorder()
	s.issueSession(rec, "account-123")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}

	tampered := *cookies[0]
	tampered.Value += "x"

	req := httptest.NewRequest(http.MethodGet, "/device", nil)
	req.AddCookie(&tampered)

	if _, ok := s.AccountFromRequest(req); ok {
		t.Fatal("AccountFromRequest() ok = true with a tampered cookie, want false")
	}
}

func TestSession_DifferentKeyRejected(t *testing.T) {
	issuer := newTestService(t)
	verifier := newTestService(t)

	rec := httptest.NewRecorder()
	issuer.issueSession(rec, "account-123")

	req := httptest.NewRequest(http.MethodGet, "/device", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	if _, ok := verifier.AccountFromRequest(req); ok {
		t.Fatal("AccountFromRequest() ok = true for a cookie signed with a different key, want false")
	}
}

func TestSession_ExpiredCookieRejected(t *testing.T) {
	s := newTestService(t)

	// Sign a payload that is already in the past directly, exercising the
	// expiry check independently of the 30-day sessionTTL.
	past := time.Now().Add(-time.Minute).Unix()
	expiredPayload := "account-123|" + strconv.FormatInt(past, 10)
	expiredCookie := &http.Cookie{Name: sessionCookieName, Value: s.signedCookieValue(expiredPayload)}

	expiredReq := httptest.NewRequest(http.MethodGet, "/device", nil)
	expiredReq.AddCookie(expiredCookie)

	if _, ok := s.AccountFromRequest(expiredReq); ok {
		t.Fatal("AccountFromRequest() ok = true for an expired session, want false")
	}
}
