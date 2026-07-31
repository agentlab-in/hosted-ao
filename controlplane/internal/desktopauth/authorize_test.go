package desktopauth

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

const (
	accountA   = "account-a"
	emailA     = "account-a@example.test"
	testOrigin = "https://ao.test"

	// signInHeader is how a test says "this request carries account X's
	// browser session", standing in for the signed cookie the auth package
	// issues. An absent or empty header is a signed-out request.
	signInHeader = "X-Test-Account"

	// testRedirectURI is the shape the desktop actually sends: loopback, an
	// ephemeral port, and the listener's /callback path.
	testRedirectURI = "http://127.0.0.1:54321/callback"

	// testVerifier is one fixed PKCE verifier, so a test can name the right
	// one and the wrong one without generating either.
	testVerifier  = "kQ7XmR3nZv1sLpTdWy8cHfJb0aOeUgN4iM2rV6xB9zA"
	wrongVerifier = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	// testState is what the app asks to have echoed back, unmodified.
	testState = "5jZ0qLwT2nX8vBcR4mHsKdYpUe1gAfN7iO3rQ6tVxZ0"
)

// oauthError is the error envelope both endpoints return, written by
// api.WriteError.
type oauthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// testSessions stands in for auth.Service on the sessions interface.
type testSessions struct{}

func (testSessions) AccountFromRequest(r *http.Request) (string, bool) {
	id := r.Header.Get(signInHeader)
	return id, id != ""
}

type harness struct {
	t   *testing.T
	svc *Service
	mux *http.ServeMux
	db  *sql.DB
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(
		`INSERT INTO accounts (id, google_subject, email, created_at) VALUES (?, ?, ?, ?)`,
		accountA, "google-"+accountA, emailA, time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	km, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}

	svc := NewService(db, tokens.NewIssuer(km, db, testOrigin, 15*time.Minute), testSessions{})
	mux := http.NewServeMux()
	svc.Register(mux)

	return &harness{t: t, svc: svc, mux: mux, db: db}
}

// authorizeQuery is a complete, valid authorization request, which each test
// then breaks in exactly one place.
func authorizeQuery() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {desktopClientID},
		"redirect_uri":          {testRedirectURI},
		"code_challenge":        {challengeFor(testVerifier)},
		"code_challenge_method": {"S256"},
		"state":                 {testState},
	}
}

// authorize sends an authorization request. account, when non-empty, signs it
// in as that account.
func (h *harness) authorize(account string, q url.Values) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/oauth/desktop/authorize?"+q.Encode(), nil)
	if account != "" {
		req.Header.Set(signInHeader, account)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// location requires a redirect and returns the parsed Location.
func (h *harness) location(rec *httptest.ResponseRecorder) *url.URL {
	h.t.Helper()

	if rec.Code != http.StatusFound {
		h.t.Fatalf("status = %d, want 302, body: %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		h.t.Fatalf("parse Location %q: %v", rec.Header().Get("Location"), err)
	}
	return u
}

// codeFrom runs a signed-in authorization request and returns the code it
// redirected back with.
func (h *harness) codeFrom(q url.Values) string {
	h.t.Helper()

	loc := h.location(h.authorize(accountA, q))
	code := loc.Query().Get("code")
	if code == "" {
		h.t.Fatalf("authorize redirected to %q with no code", loc)
	}
	return code
}

func TestAuthorize_RedirectsToTheLoopbackListenerWithACodeAndTheStateUnmodified(t *testing.T) {
	h := newHarness(t)

	loc := h.location(h.authorize(accountA, authorizeQuery()))

	if got, want := loc.Scheme+"://"+loc.Host+loc.Path, testRedirectURI; got != want {
		t.Errorf("redirected to %q, want %q", got, want)
	}
	if got := loc.Query().Get("state"); got != testState {
		t.Errorf("state = %q, want it echoed unmodified as %q", got, testState)
	}
	if got := loc.Query().Get("code"); got == "" {
		t.Error("no code in the redirect")
	}
	if got := loc.Query().Get("error"); got != "" {
		t.Errorf("error = %q, want none", got)
	}
}

// The code travels in a Location header, so nothing between here and the
// browser may keep a copy of it.
func TestAuthorize_SuccessIsNotCacheable(t *testing.T) {
	h := newHarness(t)

	rec := h.authorize(accountA, authorizeQuery())
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The stored code is a hash. A leak of controlplane.db must not hand over a
// live authorization code.
func TestAuthorize_StoresOnlyTheHashOfTheCode(t *testing.T) {
	h := newHarness(t)

	code := h.codeFrom(authorizeQuery())

	var stored, redirectURI, challenge, account string
	if err := h.db.QueryRow(
		`SELECT code_hash, redirect_uri, code_challenge, account_id FROM desktop_auth_codes`,
	).Scan(&stored, &redirectURI, &challenge, &account); err != nil {
		t.Fatalf("read stored code: %v", err)
	}
	if stored == code {
		t.Error("the plaintext authorization code was stored")
	}
	if stored != hashCode(code) {
		t.Error("the stored value is not the hash of the issued code")
	}
	// The three bindings the exchange re-checks are all fixed here.
	if redirectURI != testRedirectURI {
		t.Errorf("stored redirect_uri = %q, want %q", redirectURI, testRedirectURI)
	}
	if challenge != challengeFor(testVerifier) {
		t.Errorf("stored code_challenge = %q, want the request's challenge", challenge)
	}
	if account != accountA {
		t.Errorf("stored account_id = %q, want %q", account, accountA)
	}
}

func TestAuthorize_SignedOutGoesThroughGoogleAndComesBack(t *testing.T) {
	h := newHarness(t)

	q := authorizeQuery()
	loc := h.location(h.authorize("", q))

	if loc.Path != googleLoginPath {
		t.Fatalf("signed-out authorize went to %q, want %q", loc.Path, googleLoginPath)
	}
	next, err := url.Parse(loc.Query().Get("next"))
	if err != nil {
		t.Fatalf("parse next: %v", err)
	}
	if next.Path != "/oauth/desktop/authorize" {
		t.Errorf("next path = %q, want the authorize endpoint", next.Path)
	}
	// Every parameter has to survive the round trip, or the request that comes
	// back after Google is not the one the app made.
	for key, want := range q {
		if got := next.Query().Get(key); got != want[0] {
			t.Errorf("next lost %s: got %q, want %q", key, got, want[0])
		}
	}
	// And it must be a same-site path, or the auth package's sanitizeNext
	// discards it and the operator lands on "/" after signing in.
	if raw := loc.Query().Get("next"); len(raw) == 0 || raw[0] != '/' {
		t.Errorf("next = %q, want a relative path", raw)
	}
	if n := h.codeCount(); n != 0 {
		t.Errorf("a signed-out request issued %d codes, want 0", n)
	}
}

// The redirect URI is the one parameter that, wrong, hands an account to
// whoever crafted the link. None of these may produce a redirect at all.
func TestAuthorize_RefusesARedirectURIThatIsNotLoopback(t *testing.T) {
	cases := []struct {
		name        string
		redirectURI string
	}{
		{"a remote host", "http://evil.example/callback"},
		{"https on a remote host", "https://evil.example/callback"},
		{"a host that merely starts with the loopback literal", "http://127.0.0.1.evil.example/callback"},
		{"localhost, which is a name and not an address", "http://localhost:54321/callback"},
		{"an address that is not the loopback", "http://10.0.0.5:54321/callback"},
		{"the wildcard address", "http://0.0.0.0:54321/callback"},
		{"credentials smuggled into the authority", "http://127.0.0.1@evil.example/callback"},
		{"a port smuggled into the authority", "http://127.0.0.1:54321@evil.example/callback"},
		{"a backslash where the authority ends", `http://127.0.0.1\@evil.example/callback`},
		{"a newline, which would otherwise reach a Location header", "http://127.0.0.1:54321/callback\r\nX-Evil: 1"},
		{"a NUL", "http://127.0.0.1:54321/callback\x00"},
		{"a scheme-relative reference", "//evil.example/callback"},
		{"a relative path", "/callback"},
		{"a non-http scheme", "aodesktop://callback"},
		{"https on the loopback", "https://127.0.0.1:54321/callback"},
		{"a query of its own", "http://127.0.0.1:54321/callback?next=http://evil.example"},
		{"a fragment", "http://127.0.0.1:54321/callback#x"},
		{"an unparseable URL", "http://127.0.0.1:notaport/callback"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			q := authorizeQuery()
			q.Set("redirect_uri", tc.redirectURI)
			rec := h.authorize(accountA, q)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("redirected to %q, want no redirect at all", loc)
			}
			if n := h.codeCount(); n != 0 {
				t.Errorf("issued %d codes for a refused redirect URI, want 0", n)
			}
		})
	}
}

// RFC 8252 section 7.3: the app gets whatever ephemeral port the OS handed it,
// so every port has to work, and so does the IPv6 loopback.
func TestAuthorize_AcceptsAnyLoopbackPort(t *testing.T) {
	for _, redirectURI := range []string{
		"http://127.0.0.1:1/callback",
		"http://127.0.0.1:65535/callback",
		"http://127.0.0.1/callback",
		"http://[::1]:49152/callback",
	} {
		t.Run(redirectURI, func(t *testing.T) {
			h := newHarness(t)

			q := authorizeQuery()
			q.Set("redirect_uri", redirectURI)
			loc := h.location(h.authorize(accountA, q))

			if got := loc.Query().Get("code"); got == "" {
				t.Errorf("no code redirecting to %q", redirectURI)
			}
		})
	}
}

func TestAuthorize_RefusesAnUnknownClient(t *testing.T) {
	h := newHarness(t)

	q := authorizeQuery()
	q.Set("client_id", "not-ao-desktop")
	rec := h.authorize(accountA, q)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("redirected to %q, want no redirect", loc)
	}
}

// A callback with no state is one the app rejects before reading it, so
// redirecting would leave the user with a browser that did nothing. It is
// refused where the operator can see it instead.
func TestAuthorize_RefusesAMissingState(t *testing.T) {
	for _, name := range []string{"absent", "empty"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)

			q := authorizeQuery()
			if name == "absent" {
				q.Del("state")
			} else {
				q.Set("state", "")
			}
			rec := h.authorize(accountA, q)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("redirected to %q, want no redirect", loc)
			}
			if n := h.codeCount(); n != 0 {
				t.Errorf("issued %d codes without a state, want 0", n)
			}
		})
	}
}

// Once the redirect target is proven, the app is told what went wrong, and
// every such answer carries the state so the app can tell it apart from a
// stray hit on its port.
func TestAuthorize_ReportsABadRequestToTheAppWithItsState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{"a response type other than code", func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		{"a missing response type", func(q url.Values) { q.Del("response_type") }, "unsupported_response_type"},
		{"plain PKCE", func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		{"a missing challenge method", func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request"},
		{"a missing challenge", func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		{"a challenge that is not a SHA-256 digest", func(q url.Values) { q.Set("code_challenge", "too-short") }, "invalid_request"},
		{"a challenge that is not base64url", func(q url.Values) { q.Set("code_challenge", "++++/////+++++++++++++++++++++++++++++++++++") }, "invalid_request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			q := authorizeQuery()
			tc.mutate(q)
			loc := h.location(h.authorize(accountA, q))

			if got := loc.Query().Get("error"); got != tc.want {
				t.Errorf("error = %q, want %q", got, tc.want)
			}
			if got := loc.Query().Get("state"); got != testState {
				t.Errorf("state = %q, want %q", got, testState)
			}
			if got := loc.Query().Get("code"); got != "" {
				t.Errorf("a rejected request still carried a code")
			}
			if n := h.codeCount(); n != 0 {
				t.Errorf("issued %d codes for a rejected request, want 0", n)
			}
		})
	}
}

// The app compares state byte for byte, so anything the control plane does to
// it in transit ends the sign-in.
func TestAuthorize_EchoesAStateWithURLSignificantCharactersUnmodified(t *testing.T) {
	h := newHarness(t)

	state := "a b&c=d?e#f/g+h%i"
	q := authorizeQuery()
	q.Set("state", state)
	loc := h.location(h.authorize(accountA, q))

	if got := loc.Query().Get("state"); got != state {
		t.Errorf("state = %q, want %q", got, state)
	}
}

func TestAuthorize_EachRequestGetsItsOwnCode(t *testing.T) {
	h := newHarness(t)

	first := h.codeFrom(authorizeQuery())
	second := h.codeFrom(authorizeQuery())

	if first == second {
		t.Error("two authorization requests produced the same code")
	}
}

// codeCount is how many authorization codes are live.
func (h *harness) codeCount() int {
	h.t.Helper()

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM desktop_auth_codes`).Scan(&n); err != nil {
		h.t.Fatalf("count authorization codes: %v", err)
	}
	return n
}
