package device

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

const (
	accountA      = "account-a"
	accountB      = "account-b"
	testOrigin    = "https://ao.test"
	testPublicURL = "https://vm.example.com"
	// signInHeader is how a test says "this request carries account X's
	// browser session", standing in for the signed cookie the auth package
	// issues. An empty or absent header is a signed-out request.
	signInHeader = "X-Test-Account"
)

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

	for _, id := range []string{accountA, accountB} {
		if _, err := db.Exec(
			`INSERT INTO accounts (id, google_subject, email, created_at) VALUES (?, ?, ?, ?)`,
			id, "google-"+id, id+"@example.test", time.Now().UTC(),
		); err != nil {
			t.Fatalf("seed account %s: %v", id, err)
		}
	}

	km, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}

	svc, err := NewService(db, tokens.NewIssuer(km, db, testOrigin, 15*time.Minute), testSessions{}, testOrigin)
	if err != nil {
		t.Fatalf("NewService() unexpected error: %v", err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)

	return &harness{t: t, svc: svc, mux: mux, db: db}
}

// do sends a request through the registered mux. account, when non-empty,
// signs the request in as that account.
func (h *harness) do(method, target, account string, body url.Values) *httptest.ResponseRecorder {
	h.t.Helper()

	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if account != "" {
		req.Header.Set(signInHeader, account)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// requestCode runs the device authorization request and returns the parsed
// response.
func (h *harness) requestCode(publicURL, name string) deviceCodeResponse {
	h.t.Helper()

	form := url.Values{"public_url": {publicURL}}
	if name != "" {
		form.Set("machine_name", name)
	}
	rec := h.do(http.MethodPost, "/device/code", "", form)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("POST /device/code status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	var resp deviceCodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode device code response: %v", err)
	}
	return resp
}

// poll runs one device access token request and returns the recorder plus the
// decoded body, whichever shape it has.
func (h *harness) poll(deviceCode string) (*httptest.ResponseRecorder, tokenResponse, errorResponse) {
	h.t.Helper()

	rec := h.do(http.MethodPost, "/device/token", "", url.Values{
		"grant_type":  {deviceCodeGrantType},
		"device_code": {deviceCode},
	})
	var (
		ok      tokenResponse
		failure errorResponse
	)
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
			h.t.Fatalf("decode token response: %v", err)
		}
	} else if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		h.t.Fatalf("decode error response: %v", err)
	}
	return rec, ok, failure
}

// approve drives the two browser pages: submit the code, then approve it.
func (h *harness) approve(userCode, account string) *httptest.ResponseRecorder {
	h.t.Helper()

	if rec := h.do(http.MethodPost, "/device", account, url.Values{"user_code": {userCode}}); rec.Code != http.StatusOK {
		h.t.Fatalf("POST /device status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	return h.do(http.MethodPost, "/device/decision", account, url.Values{
		"user_code": {userCode},
		"action":    {"approve"},
	})
}

// allowNextPoll rewinds last_polled_at so the next poll is not rejected by
// the interval. Tests that are not about the interval use it to avoid
// sleeping for pollInterval.
func (h *harness) allowNextPoll() {
	h.t.Helper()
	if _, err := h.db.Exec(`UPDATE device_codes SET last_polled_at = ?`, time.Now().UTC().Add(-time.Hour)); err != nil {
		h.t.Fatalf("rewind last_polled_at: %v", err)
	}
}

// expireCodes forces every device code past its expiry.
func (h *harness) expireCodes() {
	h.t.Helper()
	if _, err := h.db.Exec(`UPDATE device_codes SET expires_at = ?`, time.Now().UTC().Add(-time.Minute)); err != nil {
		h.t.Fatalf("force-expire device codes: %v", err)
	}
}

// claimsOf pulls the `aud` and `sub` claims out of a minted access token
// without verifying it: the signature is tokens' business, the claims this
// package chose are its own.
func claimsOf(t *testing.T, token string) (aud, sub string) {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("access token has %d parts, want 3", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode access token claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal access token claims: %v", err)
	}
	return claims.Aud, claims.Sub
}

func TestDeviceFlow_HappyPathBindsTheMachineAndMintsAMachineAudienceToken(t *testing.T) {
	h := newHarness(t)

	issued := h.requestCode(testPublicURL, "prod vm")
	if issued.DeviceCode == "" || issued.UserCode == "" {
		t.Fatal("device authorization response is missing a code")
	}
	if issued.VerificationURI != testOrigin+verificationPath {
		t.Errorf("verification_uri = %q, want %q", issued.VerificationURI, testOrigin+verificationPath)
	}
	if !strings.Contains(issued.VerificationURIComplete, url.QueryEscape(issued.UserCode)) {
		t.Errorf("verification_uri_complete %q does not carry the user code", issued.VerificationURIComplete)
	}
	if issued.Interval != int(pollInterval.Seconds()) {
		t.Errorf("interval = %d, want %d", issued.Interval, int(pollInterval.Seconds()))
	}
	if issued.ExpiresIn != int(deviceCodeTTL.Seconds()) {
		t.Errorf("expires_in = %d, want %d", issued.ExpiresIn, int(deviceCodeTTL.Seconds()))
	}

	// The plaintext device code is never stored, only its hash.
	var storedDeviceCode string
	if err := h.db.QueryRow(`SELECT device_code FROM device_codes`).Scan(&storedDeviceCode); err != nil {
		t.Fatalf("read stored device code: %v", err)
	}
	if storedDeviceCode == issued.DeviceCode {
		t.Error("device_codes stores the plaintext device code, want only its hash")
	}
	if storedDeviceCode != hashCode(issued.DeviceCode) {
		t.Error("stored device code is not hashCode(plaintext)")
	}

	// Before approval, polling reports the flow is still in progress.
	rec, _, failure := h.poll(issued.DeviceCode)
	if rec.Code != http.StatusBadRequest || failure.Error != errAuthorizationPending {
		t.Fatalf("poll before approval = %d/%q, want 400/authorization_pending", rec.Code, failure.Error)
	}

	if rec := h.approve(issued.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("approval status = %d, want 200, body: %s", rec.Code, rec.Body)
	}

	var (
		machineID  string
		hostname   string
		machineAcc string
	)
	if err := h.db.QueryRow(`SELECT id, hostname, account_id FROM machines`).Scan(&machineID, &hostname, &machineAcc); err != nil {
		t.Fatalf("read registered machine: %v", err)
	}
	if machineAcc != accountA {
		t.Errorf("machine account = %q, want %q", machineAcc, accountA)
	}
	if hostname != testPublicURL {
		t.Errorf("machine hostname = %q, want %q", hostname, testPublicURL)
	}

	h.allowNextPoll()
	rec, granted, failure := h.poll(issued.DeviceCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll after approval = %d/%q, want 200", rec.Code, failure.Error)
	}

	// The triple ao setup-vm writes into machine.json.
	if granted.MachineID != machineID {
		t.Errorf("machine_id = %q, want the machines.id %q", granted.MachineID, machineID)
	}
	if granted.AccountID != accountA {
		t.Errorf("account_id = %q, want %q", granted.AccountID, accountA)
	}
	if granted.PublicURL != testPublicURL {
		t.Errorf("public_url = %q, want %q", granted.PublicURL, testPublicURL)
	}
	if granted.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", granted.TokenType)
	}
	if granted.ExpiresIn != int((15 * time.Minute).Seconds()) {
		t.Errorf("expires_in = %d, want 900", granted.ExpiresIn)
	}

	// The trap this whole task exists to avoid: the audience is machines.id,
	// never the hostname and never the public URL.
	aud, sub := claimsOf(t, granted.AccessToken)
	if sub != accountA {
		t.Errorf("access token sub = %q, want the account id %q", sub, accountA)
	}
	if aud != machineID {
		t.Errorf("access token aud = %q, want the machine id %q", aud, machineID)
	}
	if aud == testPublicURL || aud == "vm.example.com" {
		t.Fatalf("access token aud = %q, which is the machine's address: it must be machines.id", aud)
	}
}

func TestDeviceCodeEndpoint_RejectsAMissingOrUnusablePublicURL(t *testing.T) {
	h := newHarness(t)

	for _, form := range []url.Values{
		{},
		{"public_url": {""}},
		{"public_url": {"https://vm.example.com/nested/path"}},
		{"public_url": {"ftp://vm.example.com"}},
	} {
		rec := h.do(http.MethodPost, "/device/code", "", form)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /device/code with %v: status = %d, want 400", form, rec.Code)
		}
	}
}

func TestDeviceCodeEndpoint_AcceptsAJSONBody(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/device/code",
		strings.NewReader(`{"public_url":"vm.example.com","machine_name":"json vm"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	var name, publicURL string
	if err := h.db.QueryRow(`SELECT machine_name, machine_public_url FROM device_codes`).Scan(&name, &publicURL); err != nil {
		t.Fatalf("read device code row: %v", err)
	}
	if name != "json vm" || publicURL != testPublicURL {
		t.Errorf("stored (%q, %q), want (json vm, %q)", name, publicURL, testPublicURL)
	}
}

func TestDeviceCodeEndpoint_DefaultsTheMachineNameToItsHost(t *testing.T) {
	h := newHarness(t)
	h.requestCode(testPublicURL, "")

	var name string
	if err := h.db.QueryRow(`SELECT machine_name FROM device_codes`).Scan(&name); err != nil {
		t.Fatalf("read device code row: %v", err)
	}
	if name != "vm.example.com" {
		t.Errorf("machine_name = %q, want the host vm.example.com", name)
	}
}

func TestPoll_UnknownDeviceCodeIsInvalidGrant(t *testing.T) {
	h := newHarness(t)
	h.requestCode(testPublicURL, "")

	rec, _, failure := h.poll("not-a-real-device-code")
	if rec.Code != http.StatusBadRequest || failure.Error != errInvalidGrant {
		t.Errorf("poll with an unknown code = %d/%q, want 400/invalid_grant", rec.Code, failure.Error)
	}
}

func TestPoll_MissingDeviceCodeOrWrongGrantTypeIsRejected(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/device/token", "", url.Values{"grant_type": {deviceCodeGrantType}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("poll with no device_code: status = %d, want 400", rec.Code)
	}

	rec = h.do(http.MethodPost, "/device/token", "", url.Values{
		"grant_type":  {"authorization_code"},
		"device_code": {"whatever"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("poll with the wrong grant_type: status = %d, want 400", rec.Code)
	}
	var failure errorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &failure)
	if failure.Error != errUnsupportedGrantType {
		t.Errorf("error = %q, want unsupported_grant_type", failure.Error)
	}
}

func TestPoll_TooFastIsSlowDownAndDoesNotConsumeTheInterval(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")

	if _, _, failure := h.poll(issued.DeviceCode); failure.Error != errAuthorizationPending {
		t.Fatalf("first poll = %q, want authorization_pending", failure.Error)
	}

	// Immediately again, inside the interval.
	rec, _, failure := h.poll(issued.DeviceCode)
	if rec.Code != http.StatusBadRequest || failure.Error != errSlowDown {
		t.Fatalf("second poll = %d/%q, want 400/slow_down", rec.Code, failure.Error)
	}

	// A slow_down must not push last_polled_at forward: a client that ignores
	// the interval still gets through once per interval rather than being
	// locked out for as long as it keeps polling.
	var lastPolled time.Time
	if err := h.db.QueryRow(`SELECT last_polled_at FROM device_codes`).Scan(&lastPolled); err != nil {
		t.Fatalf("read last_polled_at: %v", err)
	}
	if time.Since(lastPolled) > pollInterval {
		t.Fatalf("last_polled_at is already older than the interval, the test cannot tell")
	}

	h.allowNextPoll()
	if _, _, failure := h.poll(issued.DeviceCode); failure.Error != errAuthorizationPending {
		t.Errorf("poll after the interval elapsed = %q, want authorization_pending", failure.Error)
	}
}

func TestPoll_ExpiredCodeIsExpiredTokenAndStaysExpired(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")
	h.expireCodes()

	rec, _, failure := h.poll(issued.DeviceCode)
	if rec.Code != http.StatusBadRequest || failure.Error != errExpiredToken {
		t.Fatalf("poll of an expired code = %d/%q, want 400/expired_token", rec.Code, failure.Error)
	}

	var status string
	if err := h.db.QueryRow(`SELECT status FROM device_codes`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != statusExpired {
		t.Errorf("status = %q, want %q: expiry is enforced, not just displayed", status, statusExpired)
	}

	// Expiry beats the interval: a client hammering a dead code is told to
	// stop rather than told to slow down.
	if _, _, failure := h.poll(issued.DeviceCode); failure.Error != errExpiredToken {
		t.Errorf("immediate re-poll of an expired code = %q, want expired_token", failure.Error)
	}
}

func TestApproval_ExpiredCodeCannotBeApproved(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")
	h.expireCodes()

	rec := h.do(http.MethodPost, "/device", accountA, url.Values{"user_code": {issued.UserCode}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("submitting an expired code: status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("page does not say the code expired, body: %s", rec.Body)
	}

	// Posting the decision directly, skipping the page, must not approve it
	// either: the page is a snapshot and the row is re-read on approval.
	rec = h.do(http.MethodPost, "/device/decision", accountA, url.Values{
		"user_code": {issued.UserCode},
		"action":    {"approve"},
	})
	if rec.Code == http.StatusOK {
		t.Error("approving an expired code returned 200, want it rejected")
	}
	assertNoMachines(t, h.db)
}

func TestApproval_CannotHappenTwiceEvenFromAnotherAccount(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")

	if rec := h.approve(issued.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("first approval status = %d, want 200", rec.Code)
	}

	// The second account cannot re-approve the same code and steal the
	// binding, and the machine stays with the account that approved it.
	rec := h.do(http.MethodPost, "/device/decision", accountB, url.Values{
		"user_code": {issued.UserCode},
		"action":    {"approve"},
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("second approval status = %d, want 409", rec.Code)
	}

	var (
		count     int
		accountID string
	)
	if err := h.db.QueryRow(`SELECT count(*) FROM machines`).Scan(&count); err != nil {
		t.Fatalf("count machines: %v", err)
	}
	if count != 1 {
		t.Errorf("machines rows = %d, want 1: a second approval must not register another machine", count)
	}
	if err := h.db.QueryRow(`SELECT account_id FROM device_codes`).Scan(&accountID); err != nil {
		t.Fatalf("read device code account: %v", err)
	}
	if accountID != accountA {
		t.Errorf("device code account = %q, want it still bound to %q", accountID, accountA)
	}

	// Reaching the confirmation page again fails too, so the operator is told
	// rather than shown a button that cannot work.
	rec = h.do(http.MethodPost, "/device", accountB, url.Values{"user_code": {issued.UserCode}})
	if rec.Code != http.StatusConflict {
		t.Errorf("submitting an already-approved code: status = %d, want 409", rec.Code)
	}
}

func TestApproval_RequiresASignedInSessionAndReturnsToTheCode(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")

	// A signed-out visitor following verification_uri_complete is sent
	// through sign-in and back to the prefilled form, not dead-ended.
	rec := h.do(http.MethodGet, verificationPath+"?user_code="+url.QueryEscape(issued.UserCode), "", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("signed-out GET /device status = %d, want 302", rec.Code)
	}
	location := rec.Header().Get("Location")
	next, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect %q: %v", location, err)
	}
	if next.Path != "/login" {
		t.Errorf("redirect path = %q, want /login", next.Path)
	}
	if got := next.Query().Get("next"); !strings.Contains(got, verificationPath) || !strings.Contains(got, "user_code") {
		t.Errorf("next = %q, want it to carry the code back to %s", got, verificationPath)
	}

	// A signed-out POST cannot approve.
	if rec := h.do(http.MethodPost, "/device/decision", "", url.Values{
		"user_code": {issued.UserCode},
		"action":    {"approve"},
	}); rec.Code != http.StatusFound {
		t.Errorf("signed-out approval status = %d, want a redirect to sign-in", rec.Code)
	}
	assertNoMachines(t, h.db)

	// And the code is still pollable as pending, not consumed by the attempt.
	if _, _, failure := h.poll(issued.DeviceCode); failure.Error != errAuthorizationPending {
		t.Errorf("poll after a signed-out approval attempt = %q, want authorization_pending", failure.Error)
	}
}

func TestApproval_DeniedCodeReportsAccessDenied(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "")

	rec := h.do(http.MethodPost, "/device/decision", accountA, url.Values{
		"user_code": {issued.UserCode},
		"action":    {"deny"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("deny status = %d, want 200", rec.Code)
	}
	assertNoMachines(t, h.db)

	pollRec, _, failure := h.poll(issued.DeviceCode)
	if pollRec.Code != http.StatusForbidden || failure.Error != errAccessDenied {
		t.Errorf("poll after denial = %d/%q, want 403/access_denied", pollRec.Code, failure.Error)
	}

	// A denied code cannot then be approved.
	if rec := h.do(http.MethodPost, "/device/decision", accountA, url.Values{
		"user_code": {issued.UserCode},
		"action":    {"approve"},
	}); rec.Code != http.StatusConflict {
		t.Errorf("approving a denied code: status = %d, want 409", rec.Code)
	}
	assertNoMachines(t, h.db)
}

func TestSubmitCode_UnknownCodeIsRejectedAndRateLimited(t *testing.T) {
	h := newHarness(t)

	// Wrong codes are rejected without leaking whether one exists.
	rec := h.do(http.MethodPost, "/device", accountA, url.Values{"user_code": {"WDJB-MJHT"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown code: status = %d, want 400", rec.Code)
	}

	// A short code never reaches the database.
	rec = h.do(http.MethodPost, "/device", accountA, url.Values{"user_code": {"WDJB"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed code: status = %d, want 400", rec.Code)
	}

	// The user code is short enough to guess, so guessing is capped.
	var last *httptest.ResponseRecorder
	for range attemptsPerWindow + 2 {
		last = h.do(http.MethodPost, "/device", accountA, url.Values{"user_code": {"WDJB-MJHT"}})
	}
	if last.Code != http.StatusTooManyRequests {
		t.Errorf("status after %d guesses = %d, want 429", attemptsPerWindow+2, last.Code)
	}

	// The limit is per account: it does not lock out everyone else.
	if rec := h.do(http.MethodPost, "/device", accountB, url.Values{"user_code": {"WDJB-MJHT"}}); rec.Code == http.StatusTooManyRequests {
		t.Error("a second account was rate limited by the first account's guesses")
	}
}

func TestConfirmationPage_ShowsWhatIsBeingApproved(t *testing.T) {
	h := newHarness(t)
	issued := h.requestCode(testPublicURL, "prod vm")

	rec := h.do(http.MethodPost, "/device", accountA, url.Values{"user_code": {issued.UserCode}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"prod vm", testPublicURL, issued.UserCode, `action="/device/decision"`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation page is missing %q, body: %s", want, body)
		}
	}
	// Reaching the page must not approve anything on its own.
	assertNoMachines(t, h.db)
}

func TestApproval_RebindingTheSameVMKeepsItsMachineID(t *testing.T) {
	h := newHarness(t)

	first := h.requestCode(testPublicURL, "prod vm")
	if rec := h.approve(first.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("first approval status = %d, want 200", rec.Code)
	}
	h.allowNextPoll()
	_, firstGrant, _ := h.poll(first.DeviceCode)

	// ao setup-vm is re-runnable, and a re-bind must not orphan every token
	// already minted for this box or duplicate it in the machine list.
	second := h.requestCode(testPublicURL, "prod vm renamed")
	if rec := h.approve(second.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("second approval status = %d, want 200", rec.Code)
	}
	h.allowNextPoll()
	_, secondGrant, _ := h.poll(second.DeviceCode)

	if secondGrant.MachineID != firstGrant.MachineID {
		t.Errorf("re-bind machine_id = %q, want the original %q", secondGrant.MachineID, firstGrant.MachineID)
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM machines`).Scan(&count); err != nil {
		t.Fatalf("count machines: %v", err)
	}
	if count != 1 {
		t.Errorf("machines rows = %d, want 1 after a re-bind of the same VM", count)
	}

	// Another account binding the same address gets its own machine: the
	// reuse is scoped to the account, not to the address.
	third := h.requestCode(testPublicURL, "someone else's vm")
	if rec := h.approve(third.UserCode, accountB); rec.Code != http.StatusOK {
		t.Fatalf("third approval status = %d, want 200", rec.Code)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM machines`).Scan(&count); err != nil {
		t.Fatalf("count machines: %v", err)
	}
	if count != 2 {
		t.Errorf("machines rows = %d, want 2 once a second account binds the same address", count)
	}
}

func assertNoMachines(t *testing.T, db *sql.DB) {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM machines`).Scan(&count); err != nil {
		t.Fatalf("count machines: %v", err)
	}
	if count != 0 {
		t.Errorf("machines rows = %d, want 0: nothing should have been registered", count)
	}
}

func TestListMachines_ScopedToTheCallerAndRequiresACredential(t *testing.T) {
	h := newHarness(t)

	issued := h.requestCode(testPublicURL, "prod vm")
	if rec := h.approve(issued.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("approval status = %d, want 200", rec.Code)
	}

	// No credential at all.
	rec := h.do(http.MethodGet, "/api/v1/machines", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}

	// The signed-in browser session.
	listed := h.listMachines(accountA, "")
	if len(listed) != 1 {
		t.Fatalf("account A sees %d machines, want 1", len(listed))
	}
	if listed[0].Name != "prod vm" || listed[0].PublicURL != testPublicURL {
		t.Errorf("machine = %+v, want prod vm at %s", listed[0], testPublicURL)
	}
	if listed[0].LastSeen != nil {
		t.Errorf("last_seen = %v, want null on a machine that has never checked in", listed[0].LastSeen)
	}

	// Another account's machines are not visible.
	if listed := h.listMachines(accountB, ""); len(listed) != 0 {
		t.Errorf("account B sees %d of account A's machines, want 0", len(listed))
	}
}

func TestListMachines_AcceptsARefreshTokenAndRejectsARevokedOne(t *testing.T) {
	h := newHarness(t)

	issued := h.requestCode(testPublicURL, "prod vm")
	if rec := h.approve(issued.UserCode, accountA); rec.Code != http.StatusOK {
		t.Fatalf("approval status = %d, want 200", rec.Code)
	}

	ctx := context.Background()
	refresh, err := h.svc.issuer.IssueRefreshToken(ctx, accountA, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	if listed := h.listMachines("", refresh); len(listed) != 1 {
		t.Fatalf("bearer list returned %d machines, want 1", len(listed))
	}

	// Listing must not have consumed the token: only an exchange rotates it.
	if listed := h.listMachines("", refresh); len(listed) != 1 {
		t.Errorf("second bearer list returned %d machines, want 1: listing must not rotate the token", len(listed))
	}

	// A revoked credential stops working, and so does a made-up one.
	if err := h.svc.issuer.RevokeRefreshToken(ctx, refresh); err != nil {
		t.Fatalf("RevokeRefreshToken() unexpected error: %v", err)
	}
	for _, token := range []string{refresh, "not-a-real-token"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("list with token %q: status = %d, want 401", token, rec.Code)
		}
	}
}

// listMachines calls the API with either a session account or a bearer token
// and returns the decoded list.
func (h *harness) listMachines(account, bearer string) []machine {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	if account != "" {
		req.Header.Set(signInHeader, account)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET /api/v1/machines status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	var resp machinesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode machines response: %v", err)
	}
	if resp.Machines == nil {
		h.t.Error("machines is null, want [] so a client need not special-case it")
	}
	return resp.Machines
}
