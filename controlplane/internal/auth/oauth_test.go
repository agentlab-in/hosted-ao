package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
)

func TestGeneratePKCE_ChallengeMatchesVerifier(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE() error: %v", err)
	}
	if len(verifier) < 43 {
		t.Errorf("verifier length = %d, want at least 43 per RFC 7636", len(verifier))
	}

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge = %q, want %q", challenge, want)
	}
}

func TestGeneratePKCE_Unique(t *testing.T) {
	v1, _, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE() error: %v", err)
	}
	v2, _, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE() error: %v", err)
	}
	if v1 == v2 {
		t.Error("generatePKCE() produced the same verifier twice")
	}
}

func TestSanitizeNext(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/device", "/device"},
		{"/device?code=1", "/device?code=1"},
		{"//evil.example", "/"},
		{"https://evil.example", "/"},
		{"not-a-path", "/"},
	}
	for _, tt := range tests {
		if got := sanitizeNext(tt.in); got != tt.want {
			t.Errorf("sanitizeNext(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// newTestServiceWithGoogle builds a Service wired to a fake Google backend
// (tokenHandler and userinfoHandler) instead of the real endpoints, and to a
// throwaway SQLite database with the real schema, so the exchange and
// account upsert can be exercised end to end without a network call.
func newTestServiceWithGoogle(t *testing.T, tokenHandler, userinfoHandler http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/userinfo", userinfoHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	key, err := loadOrCreateSessionKey(t.TempDir())
	if err != nil {
		t.Fatalf("loadOrCreateSessionKey() error: %v", err)
	}

	return &Service{
		db:           db,
		clientID:     "test-client-id",
		clientSecret: "test-client-secret",
		redirectURI:  "http://localhost:8080/auth/google/callback",
		endpoints: googleEndpoints{
			AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:    srv.URL + "/token",
			UserinfoURL: srv.URL + "/userinfo",
		},
		httpClient:    srv.Client(),
		sessionKey:    key,
		secureCookies: false,
	}, srv
}

func fakeGoogleHandlers(t *testing.T, subject, email string) (http.HandlerFunc, http.HandlerFunc) {
	t.Helper()

	tokenHandler := func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token request form: %v", err)
		}
		if r.FormValue("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", r.FormValue("grant_type"))
		}
		if r.FormValue("code_verifier") == "" {
			t.Error("token request missing code_verifier")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-access-token"})
	}

	userinfoHandler := func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization header = %q, want Bearer test-access-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": subject, "email": email})
	}

	return tokenHandler, userinfoHandler
}

// runLoginFlow drives handleGoogleLogin then handleGoogleCallback against s,
// simulating the browser round trip: it carries the flow cookie from the
// login redirect into the callback request and returns the callback
// response.
func runLoginFlow(t *testing.T, s *Service, next string) *http.Response {
	t.Helper()

	loginURL := "/auth/google/login"
	if next != "" {
		loginURL += "?next=" + url.QueryEscape(next)
	}
	loginReq := httptest.NewRequest(http.MethodGet, loginURL, nil)
	loginRec := httptest.NewRecorder()
	s.handleGoogleLogin(loginRec, loginReq)

	if loginRec.Code != http.StatusFound {
		t.Fatalf("handleGoogleLogin() status = %d, want %d", loginRec.Code, http.StatusFound)
	}
	redirectURL, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse login redirect: %v", err)
	}
	state := redirectURL.Query().Get("state")
	if state == "" {
		t.Fatal("login redirect missing state")
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=test-code&state="+state, nil)
	for _, c := range loginRec.Result().Cookies() {
		callbackReq.AddCookie(c)
	}
	callbackRec := httptest.NewRecorder()
	s.handleGoogleCallback(callbackRec, callbackReq)

	return callbackRec.Result()
}

func TestLoginFlow_UpsertsAccountAndIssuesSession(t *testing.T) {
	tokenHandler, userinfoHandler := fakeGoogleHandlers(t, "google-subject-1", "operator@example.test")
	s, _ := newTestServiceWithGoogle(t, tokenHandler, userinfoHandler)

	resp := runLoginFlow(t, s, "/device")

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if loc := resp.Header.Get("Location"); loc != "/device" {
		t.Errorf("callback redirect = %q, want %q", loc, "/device")
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("callback did not set a session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/device", nil)
	req.AddCookie(sessionCookie)
	accountID, ok := s.AccountFromRequest(req)
	if !ok {
		t.Fatal("AccountFromRequest() ok = false after login, want true")
	}

	var email string
	if err := s.db.QueryRow("SELECT email FROM accounts WHERE id = ?", accountID).Scan(&email); err != nil {
		t.Fatalf("query account: %v", err)
	}
	if email != "operator@example.test" {
		t.Errorf("account email = %q, want %q", email, "operator@example.test")
	}
}

// TestLoginFlow_NextSurvivesTheFlowCookieSeparator pins the round trip for a
// next that contains the character the flow cookie payload is joined on. The
// device page builds next from r.URL.RequestURI(), which carries a
// caller-supplied ?user_code=, so a crafted link used to send a signed-out
// operator into a sign-in that always failed at the callback with no
// explanation. Denial only, but silent and reachable from a link.
func TestLoginFlow_NextSurvivesTheFlowCookieSeparator(t *testing.T) {
	tokenHandler, userinfoHandler := fakeGoogleHandlers(t, "google-subject-1", "operator@example.test")
	s, _ := newTestServiceWithGoogle(t, tokenHandler, userinfoHandler)

	for _, next := range []string{
		"/device?user_code=A|B",
		"/device?user_code=|||",
		"/device",
	} {
		resp := runLoginFlow(t, s, next)
		if resp.StatusCode != http.StatusFound {
			t.Errorf("callback for next %q: status = %d, want 302", next, resp.StatusCode)
			continue
		}
		if loc := resp.Header.Get("Location"); loc != next {
			t.Errorf("callback for next %q redirected to %q, want the requested target", next, loc)
		}
	}
}

func TestLoginFlow_SecondLoginSameSubjectReusesAccountAndUpdatesEmail(t *testing.T) {
	tokenHandler1, userinfoHandler1 := fakeGoogleHandlers(t, "google-subject-1", "old@example.test")
	s, srv := newTestServiceWithGoogle(t, tokenHandler1, userinfoHandler1)

	firstResp := runLoginFlow(t, s, "")
	firstAccountID := accountIDFromResponse(t, s, firstResp)

	// Same subject, new email: re-point the fake Google backend's userinfo
	// handler at the updated profile and log in again.
	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler1)
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "google-subject-1", "email": "new@example.test"})
	})
	srv.Config.Handler = mux

	secondResp := runLoginFlow(t, s, "")
	secondAccountID := accountIDFromResponse(t, s, secondResp)

	if firstAccountID != secondAccountID {
		t.Errorf("account id changed across logins: %q then %q, want the same id", firstAccountID, secondAccountID)
	}

	var email string
	if err := s.db.QueryRow("SELECT email FROM accounts WHERE id = ?", firstAccountID).Scan(&email); err != nil {
		t.Fatalf("query account: %v", err)
	}
	if email != "new@example.test" {
		t.Errorf("account email = %q, want updated email %q", email, "new@example.test")
	}

	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM accounts").Scan(&count); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if count != 1 {
		t.Errorf("accounts table has %d rows, want 1", count)
	}
}

func accountIDFromResponse(t *testing.T, s *Service, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(c)
			accountID, ok := s.AccountFromRequest(req)
			if !ok {
				t.Fatal("session cookie from login response did not verify")
			}
			return accountID
		}
	}
	t.Fatal("login response did not set a session cookie")
	return ""
}

func TestLoginFlow_StateMismatchRejected(t *testing.T) {
	tokenHandler, userinfoHandler := fakeGoogleHandlers(t, "google-subject-1", "operator@example.test")
	s, _ := newTestServiceWithGoogle(t, tokenHandler, userinfoHandler)

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	loginRec := httptest.NewRecorder()
	s.handleGoogleLogin(loginRec, loginReq)

	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=test-code&state=wrong-state", nil)
	for _, c := range loginRec.Result().Cookies() {
		callbackReq.AddCookie(c)
	}
	callbackRec := httptest.NewRecorder()
	s.handleGoogleCallback(callbackRec, callbackReq)

	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", callbackRec.Code, http.StatusFound)
	}
	loc := callbackRec.Header().Get("Location")
	if !strings.Contains(loc, "error=state_mismatch") {
		t.Errorf("callback redirect = %q, want it to contain error=state_mismatch", loc)
	}
	for _, c := range callbackRec.Result().Cookies() {
		if c.Name == sessionCookieName {
			t.Error("callback set a session cookie despite a state mismatch")
		}
	}
}

func TestLoginFlow_MissingFlowCookieRejected(t *testing.T) {
	tokenHandler, userinfoHandler := fakeGoogleHandlers(t, "google-subject-1", "operator@example.test")
	s, _ := newTestServiceWithGoogle(t, tokenHandler, userinfoHandler)

	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=test-code&state=any", nil)
	callbackRec := httptest.NewRecorder()
	s.handleGoogleCallback(callbackRec, callbackReq)

	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", callbackRec.Code, http.StatusFound)
	}
	if loc := callbackRec.Header().Get("Location"); !strings.Contains(loc, "error=expired") {
		t.Errorf("callback redirect = %q, want it to contain error=expired", loc)
	}
}

func TestLoginFlow_GoogleErrorRejected(t *testing.T) {
	tokenHandler, userinfoHandler := fakeGoogleHandlers(t, "google-subject-1", "operator@example.test")
	s, _ := newTestServiceWithGoogle(t, tokenHandler, userinfoHandler)

	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/google/callback?error=access_denied", nil)
	callbackRec := httptest.NewRecorder()
	s.handleGoogleCallback(callbackRec, callbackReq)

	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", callbackRec.Code, http.StatusFound)
	}
	if loc := callbackRec.Header().Get("Location"); !strings.Contains(loc, "error=access_denied") {
		t.Errorf("callback redirect = %q, want it to contain error=access_denied", loc)
	}
}
