package desktopauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// tokenForm is a complete, valid exchange for code, which each test then
// breaks in exactly one place.
func tokenForm(code string) url.Values {
	return url.Values{
		"grant_type":    {authorizationCodeGrantType},
		"client_id":     {desktopClientID},
		"code":          {code},
		"code_verifier": {testVerifier},
		"redirect_uri":  {testRedirectURI},
	}
}

// exchange posts a token request and returns the raw recorder.
func (h *harness) exchange(form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/oauth/desktop/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// exchangeOK posts a token request and requires a 200, returning the decoded
// body.
func (h *harness) exchangeOK(form url.Values) tokenResponse {
	h.t.Helper()

	rec := h.exchange(form)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("POST token status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode token response: %v", err)
	}
	return resp
}

// rejects requires the exchange to fail with the collapsed invalid_grant, and
// returns the description so a caller can check every failure shares one.
func (h *harness) rejects(form url.Values) string {
	h.t.Helper()

	rec := h.exchange(form)
	if rec.Code != http.StatusBadRequest {
		h.t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body)
	}
	var body oauthError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		h.t.Fatalf("decode error body: %v", err)
	}
	if body.Error != "invalid_grant" {
		h.t.Errorf("error = %q, want invalid_grant", body.Error)
	}
	return body.ErrorDescription
}

// refreshTokenCount is how many refresh tokens exist. The number that matters
// after a replay is one.
func (h *harness) refreshTokenCount() int {
	h.t.Helper()

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens`).Scan(&n); err != nil {
		h.t.Fatalf("count refresh tokens: %v", err)
	}
	return n
}

func TestToken_ExchangesTheCodeForTheAccountAndARefreshToken(t *testing.T) {
	h := newHarness(t)

	resp := h.exchangeOK(tokenForm(h.codeFrom(authorizeQuery())))

	if resp.Account.ID != accountA {
		t.Errorf("account.id = %q, want %q", resp.Account.ID, accountA)
	}
	if resp.Account.Email != emailA {
		t.Errorf("account.email = %q, want %q", resp.Account.Email, emailA)
	}
	if resp.RefreshToken == "" {
		t.Fatal("no refresh token in the response")
	}
	if n := h.refreshTokenCount(); n != 1 {
		t.Errorf("refresh token rows = %d, want 1", n)
	}
	// The contract is explicit: login yields identity plus the refresh token
	// and nothing else. Every access token comes from POST /api/v1/token.
	if body := h.exchangeBody(t); strings.Contains(body, "access_token") {
		t.Errorf("the exchange returned an access token: %s", body)
	}
}

// exchangeBody runs one more exchange and returns its raw body, so a test can
// assert on fields the response struct does not have.
func (h *harness) exchangeBody(t *testing.T) string {
	t.Helper()

	rec := h.exchange(tokenForm(h.codeFrom(authorizeQuery())))
	return rec.Body.String()
}

// The refresh token is a bearer credential for ninety days. Only its hash may
// reach disk, and the response must not be cached.
func TestToken_StoresOnlyTheHashOfTheRefreshToken(t *testing.T) {
	h := newHarness(t)

	resp := h.exchangeOK(tokenForm(h.codeFrom(authorizeQuery())))

	var stored string
	if err := h.db.QueryRow(`SELECT token_hash FROM refresh_tokens`).Scan(&stored); err != nil {
		t.Fatalf("read refresh token row: %v", err)
	}
	if stored == resp.RefreshToken {
		t.Error("the plaintext refresh token was stored")
	}
}

func TestToken_ResponseIsNotCacheable(t *testing.T) {
	h := newHarness(t)

	rec := h.exchange(tokenForm(h.codeFrom(authorizeQuery())))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A replayed code must mint nothing. The code is consumed in the same
// transaction that issues the refresh token, so the second attempt finds no
// row and there is still exactly one refresh token.
func TestToken_ARepliedCodeMintsNothing(t *testing.T) {
	h := newHarness(t)
	form := tokenForm(h.codeFrom(authorizeQuery()))

	first := h.exchangeOK(form)
	h.rejects(form)

	if n := h.refreshTokenCount(); n != 1 {
		t.Errorf("refresh token rows after a replay = %d, want 1", n)
	}
	if n := h.codeCount(); n != 0 {
		t.Errorf("authorization codes after redemption = %d, want 0", n)
	}
	// And the one row that survives is the token the first exchange handed
	// out, not a second one the replay slipped in: it still rotates.
	if _, _, _, err := h.svc.issuer.RotateRefreshToken(context.Background(), first.RefreshToken); err != nil {
		t.Errorf("the refresh token from the first exchange no longer works: %v", err)
	}
}

// The same replay, raced. Whatever the interleaving, one caller wins and one
// refresh token exists.
func TestToken_ConcurrentRedemptionsOfOneCodeMintOneRefreshToken(t *testing.T) {
	h := newHarness(t)
	form := tokenForm(h.codeFrom(authorizeQuery()))

	const attempts = 4
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes []int
	)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := h.exchange(form)
			mu.Lock()
			codes = append(codes, rec.Code)
			mu.Unlock()
		}()
	}
	wg.Wait()

	won := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			won++
		case http.StatusBadRequest:
		default:
			t.Errorf("unexpected status %d, want 200 or 400", c)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d concurrent redemptions succeeded, want exactly 1", won, attempts)
	}
	if n := h.refreshTokenCount(); n != 1 {
		t.Errorf("refresh token rows = %d, want 1", n)
	}
}

// Every way the exchange can fail is one invalid_grant with one description.
// An unknown code, an expired one, a replayed one, a mismatched redirect URI,
// and a wrong verifier must be indistinguishable, or this endpoint is an
// oracle for whoever holds a stolen code.
func TestToken_EveryFailureIsTheSameInvalidGrant(t *testing.T) {
	descriptions := map[string]string{}

	cases := []struct {
		name string
		form func(h *harness) url.Values
	}{
		{"an unknown code", func(h *harness) url.Values {
			return tokenForm("nosuchcodenosuchcodenosuchcodenosuchcode")
		}},
		{"a wrong verifier", func(h *harness) url.Values {
			f := tokenForm(h.codeFrom(authorizeQuery()))
			f.Set("code_verifier", wrongVerifier)
			return f
		}},
		{"a redirect URI that is not the one the code was issued for", func(h *harness) url.Values {
			f := tokenForm(h.codeFrom(authorizeQuery()))
			f.Set("redirect_uri", "http://127.0.0.1:54322/callback")
			return f
		}},
		{"the same redirect URI spelled differently", func(h *harness) url.Values {
			f := tokenForm(h.codeFrom(authorizeQuery()))
			f.Set("redirect_uri", testRedirectURI+"/")
			return f
		}},
		{"a replayed code", func(h *harness) url.Values {
			f := tokenForm(h.codeFrom(authorizeQuery()))
			h.exchangeOK(f)
			return f
		}},
		{"an expired code", func(h *harness) url.Values {
			return tokenForm(h.expiredCode())
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// After the setup, not before: the replayed-code case spends a code
			// legitimately on its way to arranging the replay.
			form := tc.form(h)
			before := h.refreshTokenCount()

			descriptions[tc.name] = h.rejects(form)

			if got := h.refreshTokenCount(); got != before {
				t.Errorf("a rejected exchange minted %d refresh tokens", got-before)
			}
		})
	}

	var seen string
	for name, descr := range descriptions {
		if seen == "" {
			seen = descr
			continue
		}
		if descr != seen {
			t.Errorf("%s answered %q, want the same description as every other failure, %q", name, descr, seen)
		}
	}
}

// A bad verifier or redirect URI must not consume the code: the code alone is
// useless without the verifier, so burning it on a failed attempt would only
// let anyone who intercepted it deny the real app its sign-in.
func TestToken_AFailedAttemptDoesNotBurnTheCode(t *testing.T) {
	h := newHarness(t)
	form := tokenForm(h.codeFrom(authorizeQuery()))

	wrong := tokenForm(form.Get("code"))
	wrong.Set("code_verifier", wrongVerifier)
	h.rejects(wrong)

	if n := h.codeCount(); n != 1 {
		t.Fatalf("authorization codes after a failed attempt = %d, want 1", n)
	}
	if resp := h.exchangeOK(form); resp.RefreshToken == "" {
		t.Error("the real exchange did not complete after a failed attempt")
	}
}

// An expired code is refused even before the sweep has removed its row.
func TestToken_RefusesAnExpiredCode(t *testing.T) {
	h := newHarness(t)

	h.rejects(tokenForm(h.expiredCode()))

	if n := h.refreshTokenCount(); n != 0 {
		t.Errorf("an expired code minted %d refresh tokens, want 0", n)
	}
}

// expiredCode issues a code whose whole lifetime is already in the past.
func (h *harness) expiredCode() string {
	h.t.Helper()

	past := time.Now().UTC().Add(-authCodeTTL - time.Minute)
	code, err := h.svc.createCode(context.Background(), accountA, testRedirectURI, challengeFor(testVerifier), past)
	if err != nil {
		h.t.Fatalf("create expired code: %v", err)
	}
	return code
}

func TestToken_SweepsExpiredCodes(t *testing.T) {
	h := newHarness(t)
	h.expiredCode()

	if n := h.codeCount(); n != 1 {
		t.Fatalf("expired codes before a sweep = %d, want 1", n)
	}
	// Issuing the next code sweeps.
	h.codeFrom(authorizeQuery())

	if n := h.codeCount(); n != 1 {
		t.Errorf("codes after the sweep = %d, want only the fresh one", n)
	}
}

// A malformed request is told what is wrong, because none of it depends on a
// credential. Only the exchange itself collapses.
func TestToken_RejectsAMalformedRequest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{"a missing grant type", func(f url.Values) { f.Del("grant_type") }, "unsupported_grant_type"},
		{"the wrong grant type", func(f url.Values) { f.Set("grant_type", "refresh_token") }, "unsupported_grant_type"},
		{"an unknown client", func(f url.Values) { f.Set("client_id", "not-ao-desktop") }, "invalid_client"},
		{"a missing client", func(f url.Values) { f.Del("client_id") }, "invalid_client"},
		{"a missing code", func(f url.Values) { f.Del("code") }, "invalid_request"},
		{"a missing verifier", func(f url.Values) { f.Del("code_verifier") }, "invalid_request"},
		{"a missing redirect URI", func(f url.Values) { f.Del("redirect_uri") }, "invalid_request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			form := tokenForm(h.codeFrom(authorizeQuery()))
			tc.mutate(form)

			rec := h.exchange(form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body)
			}
			var body oauthError
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Error != tc.want {
				t.Errorf("error = %q, want %q", body.Error, tc.want)
			}
			// Nothing that failed validation may have touched the code.
			if n := h.codeCount(); n != 1 {
				t.Errorf("authorization codes = %d, want the unspent one", n)
			}
		})
	}
}

// A code issued for one account never yields another account's refresh token:
// the identity comes from the stored row, and the token request carries no
// account at all.
func TestToken_BindsTheRefreshTokenToTheCodesOwnAccount(t *testing.T) {
	h := newHarness(t)

	resp := h.exchangeOK(tokenForm(h.codeFrom(authorizeQuery())))

	var account string
	if err := h.db.QueryRow(`SELECT account_id FROM refresh_tokens`).Scan(&account); err != nil {
		t.Fatalf("read refresh token row: %v", err)
	}
	if account != accountA {
		t.Errorf("refresh token account = %q, want %q", account, accountA)
	}
	if resp.Account.ID != account {
		t.Errorf("response account %q does not match the token's account %q", resp.Account.ID, account)
	}
}

func TestToken_RateLimitsTheEndpoint(t *testing.T) {
	h := newHarness(t)

	// Unknown codes, so nothing is spent: this measures the limiter, not the
	// exchange.
	form := tokenForm("nosuchcodenosuchcodenosuchcodenosuchcode")
	limited := false
	for range tokenAttemptsPerWindow + 1 {
		if h.exchange(form).Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Errorf("no request was limited within %d attempts", tokenAttemptsPerWindow+1)
	}
}

// Behind the loopback reverse proxy this service runs under, every RemoteAddr
// is the proxy, so one caller must not be able to spend everybody's allowance.
// Caddy appends the address it saw, so the last hop is the one entry a client
// cannot forge.
func TestClientKey(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"a direct caller", "198.51.100.7:4021", "", "198.51.100.7"},
		{"a direct caller cannot forge a hop", "198.51.100.7:4021", "203.0.113.9", "198.51.100.7"},
		{"behind the local proxy", "127.0.0.1:8080", "203.0.113.9", "203.0.113.9"},
		{"the client's spoofed hops are ignored", "127.0.0.1:8080", "1.2.3.4, 5.6.7.8, 203.0.113.9", "203.0.113.9"},
		{"the proxy sent no header", "127.0.0.1:8080", "", "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/oauth/desktop/token", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if got := clientKey(req); got != tt.want {
				t.Errorf("clientKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWindowLimiter_ResetsOnTheNextWindow(t *testing.T) {
	now := time.Now()
	l := newWindowLimiter()
	l.clock = func() time.Time { return now }

	for i := range tokenAttemptsPerWindow {
		if !l.allow("a") {
			t.Fatalf("attempt %d refused inside the allowance", i+1)
		}
	}
	if l.allow("a") {
		t.Error("the attempt past the allowance was permitted")
	}
	// A different caller has its own allowance.
	if !l.allow("b") {
		t.Error("a second caller was refused on the first caller's count")
	}

	now = now.Add(tokenWindow)
	if !l.allow("a") {
		t.Error("the allowance did not reset on the next window")
	}
}
