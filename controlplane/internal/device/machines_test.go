package device

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

// listMachinesRaw sends one machines request with the given bearer credential
// (empty for none) and returns the raw recorder.
func (h *harness) listMachinesRaw(bearer string) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// bindMachine runs a whole device flow for account and returns the machine id
// it registered.
func (h *harness) bindMachine(account, publicURL, name string) string {
	h.t.Helper()

	issued := h.requestCode(publicURL, name)
	if rec := h.approve(issued.UserCode, account); rec.Code != http.StatusOK {
		h.t.Fatalf("approval status = %d, want 200, body: %s", rec.Code, rec.Body)
	}
	h.allowNextPoll()
	_, granted, _ := h.poll(issued.DeviceCode)
	if granted.MachineID == "" {
		h.t.Fatal("poll after approval returned no machine id")
	}
	return granted.MachineID
}

// controlPlaneToken mints the credential the desktop calls the control plane
// API with: an access token whose aud is the control plane's own origin.
func (h *harness) controlPlaneToken(account string) string {
	h.t.Helper()

	token, err := h.svc.issuer.IssueControlPlaneToken(account)
	if err != nil {
		h.t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	return token
}

func TestListMachines_ScopedToTheTokensAccount(t *testing.T) {
	h := newHarness(t)
	h.bindMachine(accountA, testPublicURL, "prod vm")

	listed := h.listMachines(h.controlPlaneToken(accountA))
	if len(listed) != 1 {
		t.Fatalf("account A sees %d machines, want 1", len(listed))
	}
	if listed[0].Name != "prod vm" || listed[0].PublicURL != testPublicURL {
		t.Errorf("machine = %+v, want prod vm at %s", listed[0], testPublicURL)
	}
	if listed[0].LastSeen != nil {
		t.Errorf("last_seen = %v, want null on a machine that has never checked in", listed[0].LastSeen)
	}

	// Another account's token does not reach account A's machines.
	if listed := h.listMachines(h.controlPlaneToken(accountB)); len(listed) != 0 {
		t.Errorf("account B sees %d of account A's machines, want 0", len(listed))
	}
}

func TestRevokeMachine_RemovesFromListAndBlocksTokens(t *testing.T) {
	h := newHarness(t)
	machineID := h.bindMachine(accountA, testPublicURL, "prod vm")
	credential := h.controlPlaneToken(accountA)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/"+machineID, nil)
	req.Header.Set("Authorization", "Bearer "+credential)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204, body: %s", rec.Code, rec.Body)
	}

	if listed := h.listMachines(credential); len(listed) != 0 {
		t.Fatalf("list after revoke: %d machines, want 0", len(listed))
	}
	if rec := h.machineTokenRaw(machineID, credential); rec.Code != http.StatusNotFound {
		t.Fatalf("token after revoke: status = %d, want 404", rec.Code)
	}

	// Second DELETE is the same not-found as an unknown id.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/"+machineID, nil)
	req2.Header.Set("Authorization", "Bearer "+credential)
	rec2 := httptest.NewRecorder()
	h.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", rec2.Code)
	}
}

func TestRevokeMachine_RequiresControlPlaneToken(t *testing.T) {
	h := newHarness(t)
	machineID := h.bindMachine(accountA, testPublicURL, "prod vm")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/"+machineID, nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated DELETE status = %d, want 401", rec.Code)
	}
}

func TestListMachines_RequiresAControlPlaneAudienceToken(t *testing.T) {
	h := newHarness(t)
	machineID := h.bindMachine(accountA, testPublicURL, "prod vm")
	ctx := context.Background()

	// A machine-audience token is a credential for that machine's gateway,
	// not for this service. It is validly signed by the same key, which is
	// exactly why the audience check has to be the thing that rejects it.
	machineToken, err := h.svc.issuer.IssueAccessToken(accountA, machineID)
	if err != nil {
		t.Fatalf("IssueAccessToken() unexpected error: %v", err)
	}

	// A refresh token goes only to the token endpoint, never to a resource
	// route: it is long-lived, so accepting it here would put ninety days of
	// account access on every call.
	refresh, err := h.svc.issuer.IssueRefreshToken(ctx, accountA, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	// A token from a different control plane: right shape, right claims,
	// wrong signing key.
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
		rec := h.listMachinesRaw(credential)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Errorf("%s: WWW-Authenticate = %q, want a Bearer challenge", name, got)
		}
	}

	// The browser session alone is not a credential for the API either: the
	// desktop cannot send one, and accepting an ambient cookie on the API
	// would make it the weakest thing guarding the route.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	req.Header.Set(signInHeader, accountA)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session cookie only: status = %d, want 401", rec.Code)
	}

	// The credential that is meant for this route still works, so none of the
	// above is passing for the wrong reason.
	if rec := h.listMachinesRaw(valid); rec.Code != http.StatusOK {
		t.Errorf("control-plane-audience token: status = %d, want 200", rec.Code)
	}
}

// expiredIssuer mints with this harness's signing keys but a TTL so short
// that every token it produces is already past its exp.
func (h *harness) expiredIssuer() *tokens.Issuer {
	return tokens.NewIssuer(h.km, h.db, testOrigin, time.Nanosecond)
}

func TestListMachines_ExpiredTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	h.bindMachine(accountA, testPublicURL, "prod vm")

	// A TTL this short is not reachable through config (10m is the floor),
	// but the verifier must not depend on that: exp is checked, always.
	expired, err := h.expiredIssuer().IssueControlPlaneToken(accountA)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	if rec := h.listMachinesRaw(expired); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token: status = %d, want 401", rec.Code)
	}
}
