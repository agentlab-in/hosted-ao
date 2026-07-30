package api

import (
	"context"
	"database/sql"
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
	testAccountID = "account-a"
	testOrigin    = "https://ao.test"
)

func newTestService(t *testing.T) (*Service, *tokens.Issuer, *sql.DB) {
	t.Helper()

	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(
		`INSERT INTO accounts (id, google_subject, email, created_at) VALUES (?, ?, ?, ?)`,
		testAccountID, "google-subject", "a@example.test", time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	km, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}

	issuer := tokens.NewIssuer(km, db, testOrigin, 15*time.Minute)
	return NewService(issuer), issuer, db
}

func exchange(t *testing.T, s *Service, form url.Values) (*httptest.ResponseRecorder, tokenResponse) {
	t.Helper()

	mux := http.NewServeMux()
	s.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body tokenResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode token response: %v", err)
		}
	}
	return rec, body
}

func TestTokenEndpoint_ExchangesARefreshTokenForAControlPlaneToken(t *testing.T) {
	svc, issuer, _ := newTestService(t)
	ctx := context.Background()

	refresh, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	rec, body := exchange(t, svc, url.Values{
		"grant_type":    {refreshGrantType},
		"refresh_token": {refresh},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	if body.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", body.TokenType)
	}
	if body.ExpiresIn != int((15 * time.Minute).Seconds()) {
		t.Errorf("expires_in = %d, want 900", body.ExpiresIn)
	}

	// The access token authenticates this account against the control plane.
	accountID, err := issuer.VerifyControlPlaneToken(body.AccessToken)
	if err != nil {
		t.Fatalf("the issued access token does not verify: %v", err)
	}
	if accountID != testAccountID {
		t.Errorf("token account = %q, want %q", accountID, testAccountID)
	}

	// The refresh token rotated, and the presented one is spent.
	if body.RefreshToken == "" || body.RefreshToken == refresh {
		t.Error("refresh_token did not rotate, want a replacement the caller must persist")
	}
	if rec, _ := exchange(t, svc, url.Values{"refresh_token": {refresh}}); rec.Code != http.StatusBadRequest {
		t.Errorf("replaying the spent refresh token: status = %d, want 400", rec.Code)
	}

	// The replacement works, so rotation did not strand the caller.
	if rec, _ := exchange(t, svc, url.Values{"refresh_token": {body.RefreshToken}}); rec.Code != http.StatusOK {
		t.Errorf("exchanging the rotated token: status = %d, want 200", rec.Code)
	}
}

func TestTokenEndpoint_RejectsBadRequestsIdentically(t *testing.T) {
	svc, issuer, _ := newTestService(t)
	ctx := context.Background()

	revoked, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}
	if err := issuer.RevokeRefreshToken(ctx, revoked); err != nil {
		t.Fatalf("RevokeRefreshToken() unexpected error: %v", err)
	}

	// A revoked token and an unknown one must be indistinguishable, or the
	// endpoint reports which tokens ever existed.
	for _, presented := range []string{revoked, "not-a-real-token"} {
		rec, _ := exchange(t, svc, url.Values{"refresh_token": {presented}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("refresh_token %q: status = %d, want 400", presented, rec.Code)
		}
		var body struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error != "invalid_grant" {
			t.Errorf("error = %q, want invalid_grant", body.Error)
		}
		if strings.Contains(body.ErrorDescription, "revoke") {
			t.Errorf("error_description %q distinguishes a revoked token from an unknown one", body.ErrorDescription)
		}
	}

	if rec, _ := exchange(t, svc, url.Values{"grant_type": {refreshGrantType}}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing refresh_token: status = %d, want 400", rec.Code)
	}
	if rec, _ := exchange(t, svc, url.Values{
		"grant_type":    {"authorization_code"},
		"refresh_token": {"whatever"},
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong grant_type: status = %d, want 400", rec.Code)
	}
}

func TestAuthenticate_AcceptsOnlyAControlPlaneAudienceBearerToken(t *testing.T) {
	svc, issuer, _ := newTestService(t)
	ctx := context.Background()

	valid, err := issuer.IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	machineToken, err := issuer.IssueAccessToken(testAccountID, "machine-1")
	if err != nil {
		t.Fatalf("IssueAccessToken() unexpected error: %v", err)
	}
	refresh, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	rejected := map[string]string{
		"machine audience":   "Bearer " + machineToken,
		"refresh token":      "Bearer " + refresh,
		"no scheme":          valid,
		"wrong scheme":       "Basic " + valid,
		"empty value":        "Bearer ",
		"nothing at all":     "",
		"tampered signature": "Bearer " + valid[:strings.LastIndex(valid, ".")] + ".AAAA",
	}
	for name, header := range rejected {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if _, ok := svc.Authenticate(req); ok {
			t.Errorf("%s was accepted, want it rejected", name)
		}
	}

	// The scheme is case-insensitive per RFC 7235, and the right credential
	// resolves the right account.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set("Authorization", "bearer "+valid)
	accountID, ok := svc.Authenticate(req)
	if !ok {
		t.Fatal("a control-plane-audience token was rejected")
	}
	if accountID != testAccountID {
		t.Errorf("accountID = %q, want %q", accountID, testAccountID)
	}
}

func TestUnauthorized_ChallengesWithBearer(t *testing.T) {
	rec := httptest.NewRecorder()
	Unauthorized(rec)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestReadParams_AcceptsJSONAndIgnoresNonStrings(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/token",
		strings.NewReader(`{"refresh_token":"abc","expires_in":900,"nested":{"a":1}}`))
	req.Header.Set("Content-Type", "application/json")

	params, err := ReadParams(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("ReadParams() unexpected error: %v", err)
	}
	if got := params.Get("refresh_token"); got != "abc" {
		t.Errorf("refresh_token = %q, want abc", got)
	}
	for _, key := range []string{"expires_in", "nested"} {
		if params.Has(key) {
			t.Errorf("%s was read from a non-string JSON value, want it ignored", key)
		}
	}
}
