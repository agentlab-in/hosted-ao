package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// machineTokenRaw asks for a token for machineID with the given bearer
// credential (empty for none) and returns the raw recorder.
func (h *harness) machineTokenRaw(machineID, bearer string) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/machines/"+machineID+"/token", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// machineToken asks for a token and requires a 200, returning the decoded body.
func (h *harness) machineToken(machineID, bearer string) machineTokenResponse {
	h.t.Helper()

	rec := h.machineTokenRaw(machineID, bearer)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("POST machine token status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	var resp machineTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode machine token response: %v", err)
	}
	return resp
}

// revokeMachine sets revoked_at on a machine through the public API.
func (h *harness) revokeMachine(machineID string) {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/"+machineID, nil)
	req.Header.Set("Authorization", "Bearer "+h.controlPlaneToken(accountA))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		h.t.Fatalf("DELETE machine status = %d, want 204, body: %s", rec.Code, rec.Body)
	}
}

func TestMachineToken_MintsAMachineAudienceTokenForTheCallersMachine(t *testing.T) {
	h := newHarness(t)
	machineID := h.bindMachine(accountA, testPublicURL, "prod vm")

	granted := h.machineToken(machineID, h.controlPlaneToken(accountA))
	if granted.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", granted.TokenType)
	}
	if want := int(h.svc.issuer.AccessTokenTTL().Seconds()); granted.ExpiresIn != want {
		t.Errorf("expires_in = %d, want %d", granted.ExpiresIn, want)
	}

	// The trap this whole endpoint exists to avoid: the audience is
	// machines.id, never the hostname and never the public URL.
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

	// The other half of the two-audience contract: what comes back here is a
	// credential for that machine's gateway and not for this service, so
	// presenting it to the control plane API fails the audience check.
	if _, err := h.svc.issuer.VerifyControlPlaneToken(granted.AccessToken); err == nil {
		t.Error("the machine token verifies as a control plane token, want it rejected on aud")
	}
	if rec := h.listMachinesRaw(granted.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("machine token on the machines API: status = %d, want 401", rec.Code)
	}

	// No refresh token is presented and none rotates: the caller can ask
	// again with the same credential.
	if again := h.machineToken(machineID, h.controlPlaneToken(accountA)); again.AccessToken == "" {
		t.Error("a second request returned no access token")
	}
}

func TestMachineToken_RequiresAControlPlaneAudienceToken(t *testing.T) {
	h := newHarness(t)
	machineID := h.bindMachine(accountA, testPublicURL, "prod vm")
	ctx := context.Background()

	// A machine-audience token is a credential for that machine's gateway, not
	// for this service. Accepting one here would let a leaked machine token
	// mint fresh machine tokens, including for the account's other machines.
	machineToken, err := h.svc.issuer.IssueAccessToken(accountA, machineID)
	if err != nil {
		t.Fatalf("IssueAccessToken() unexpected error: %v", err)
	}

	// A refresh token goes only to the token endpoint. This route deliberately
	// does not take one: it is called whenever the desktop switches machines or
	// a 15 minute token lapses, and rotating a 90 day credential on every one
	// of those is the lockout race this design avoids.
	refresh, err := h.svc.issuer.IssueRefreshToken(ctx, accountA, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	// A token from a different control plane: right shape, right claims, wrong
	// signing key.
	foreign := newHarness(t).controlPlaneToken(accountA)

	valid := h.controlPlaneToken(accountA)
	tampered := valid[:strings.LastIndex(valid, ".")] + ".AAAA"

	for name, credential := range map[string]string{
		"no credential":         "",
		"machine audience":      machineToken,
		"refresh token":         refresh,
		"another control plane": foreign,
		"tampered signature":    tampered,
		"not a token at all":    "nonsense",
		"empty bearer value":    " ",
	} {
		rec := h.machineTokenRaw(machineID, credential)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Errorf("%s: WWW-Authenticate = %q, want a Bearer challenge", name, got)
		}
	}

	// An expired token is rejected too: exp is checked, always.
	expired, err := h.expiredIssuer().IssueControlPlaneToken(accountA)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	if rec := h.machineTokenRaw(machineID, expired); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token: status = %d, want 401", rec.Code)
	}

	// The browser session alone is not a credential for the API either: the
	// desktop cannot send one, and accepting an ambient cookie on a POST would
	// make this route CSRF-reachable.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/machines/"+machineID+"/token", nil)
	req.Header.Set(signInHeader, accountA)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session cookie only: status = %d, want 401", rec.Code)
	}

	// The credential that is meant for this route still works, so none of the
	// above is passing for the wrong reason.
	if rec := h.machineTokenRaw(machineID, valid); rec.Code != http.StatusOK {
		t.Errorf("control-plane-audience token: status = %d, want 200", rec.Code)
	}
}

// TestMachineToken_UnownedMachinesAreIndistinguishable is the point of the
// whole handler: a signed-in account must not be able to learn which machine
// ids exist or who owns them. Someone else's machine, a revoked one, and one
// that never existed all answer identically.
func TestMachineToken_UnownedMachinesAreIndistinguishable(t *testing.T) {
	h := newHarness(t)

	mine := h.bindMachine(accountA, testPublicURL, "prod vm")
	theirs := h.bindMachine(accountB, "https://vm.other.example.com", "their vm")
	revoked := h.bindMachine(accountA, "https://vm.retired.example.com", "retired vm")
	h.revokeMachine(revoked)

	credential := h.controlPlaneToken(accountA)
	answers := map[string]*httptest.ResponseRecorder{
		"another account's machine":            h.machineTokenRaw(theirs, credential),
		"a revoked machine":                    h.machineTokenRaw(revoked, credential),
		"an unknown machine id":                h.machineTokenRaw("00000000-0000-0000-0000-000000000000", credential),
		"a machine id that is not even a uuid": h.machineTokenRaw("not-a-machine", credential),
	}

	want := answers["an unknown machine id"]
	for name, rec := range answers {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, rec.Code)
		}
		if rec.Body.String() != want.Body.String() {
			t.Errorf("%s: body = %s, want the same body as an unknown machine id: %s", name, rec.Body, want.Body)
		}
		if got := rec.Header().Get("Content-Type"); got != want.Header().Get("Content-Type") {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, want.Header().Get("Content-Type"))
		}
	}

	// Not passing for the wrong reason: the account's own live machine works.
	if rec := h.machineTokenRaw(mine, credential); rec.Code != http.StatusOK {
		t.Errorf("the caller's own machine: status = %d, want 200", rec.Code)
	}
	// And the owner of the machine account A could not reach still can.
	if rec := h.machineTokenRaw(theirs, h.controlPlaneToken(accountB)); rec.Code != http.StatusOK {
		t.Errorf("account B's own machine: status = %d, want 200", rec.Code)
	}
	// A revoked machine is refused to its own owner too, not merely hidden
	// from strangers.
	if rec := h.machineTokenRaw(revoked, credential); rec.Code != http.StatusNotFound {
		t.Errorf("the owner's revoked machine: status = %d, want 404", rec.Code)
	}
}

// TestMachineToken_EmptyMachineIDDoesNotFallThrough guards the route pattern:
// an empty path segment must not reach the handler with an empty id, where a
// query for account_id alone could match a row.
func TestMachineToken_EmptyMachineIDDoesNotFallThrough(t *testing.T) {
	h := newHarness(t)
	h.bindMachine(accountA, testPublicURL, "prod vm")

	rec := h.machineTokenRaw("", h.controlPlaneToken(accountA))
	if rec.Code == http.StatusOK {
		t.Errorf("POST /api/v1/machines//token returned 200 and body %s, want a rejection", rec.Body)
	}
}
