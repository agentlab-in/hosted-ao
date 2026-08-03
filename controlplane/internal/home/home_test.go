package home

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testOrigin = "https://ao.agentlab.in"

// fakeSessions signs a request in, or does not.
type fakeSessions struct{ signedIn bool }

func (f fakeSessions) AccountFromRequest(*http.Request) (string, bool) {
	return "account-1", f.signedIn
}

// fakeMachines is an in-memory registry for the home page tests.
type fakeMachines struct {
	mu       sync.Mutex
	machines []Machine
}

func (f *fakeMachines) ListMachines(context.Context, string) ([]Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Machine, len(f.machines))
	copy(out, f.machines)
	return out, nil
}

func (f *fakeMachines) RevokeMachine(_ context.Context, _, machineID string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.machines {
		if m.ID == machineID {
			f.machines = append(f.machines[:i], f.machines[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func newTestMux(t *testing.T, signedIn bool, m Machines) *http.ServeMux {
	t.Helper()
	if m == nil {
		m = &fakeMachines{}
	}
	svc, err := NewService(fakeSessions{signedIn: signedIn}, m, testOrigin)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	return mux
}

func get(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestSignedInLandingPage is the whole point of the page: sign-in redirects to
// "/", and "/" must not 404.
func TestSignedInLandingPage(t *testing.T) {
	w := get(newTestMux(t, true, nil), "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), `href="/device"`) {
		t.Fatalf("landing page does not link to /device:\n%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No machines yet") {
		t.Fatalf("empty state missing setup copy:\n%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ao setup-vm") {
		t.Fatalf("empty state missing setup-vm command:\n%s", w.Body.String())
	}
}

func TestSignedInListsMachines(t *testing.T) {
	reg := &fakeMachines{machines: []Machine{{
		ID:        "mch-1",
		Name:      "prod vm",
		PublicURL: "https://vm.example.com",
		CreatedAt: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	}}}
	w := get(newTestMux(t, true, reg), "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "prod vm") {
		t.Fatalf("machine name missing:\n%s", body)
	}
	if !strings.Contains(body, "https://vm.example.com") {
		t.Fatalf("public URL missing:\n%s", body)
	}
	if !strings.Contains(body, `name="machine_id" value="mch-1"`) {
		t.Fatalf("unbind form missing machine id:\n%s", body)
	}
	if strings.Contains(body, "No machines yet") {
		t.Fatalf("empty state shown with machines present:\n%s", body)
	}
}

// TestUnauthenticatedRedirectsToLogin: the deliberate choice for an anonymous
// GET /. Not a 404, and not a public page, because everything reachable from
// here needs an account.
func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	w := get(newTestMux(t, false, nil), "/")

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
	}
	if got := w.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
}

// TestOnlyMatchesRoot guards the "/{$}" pattern: a bare "/" would claim every
// path no other handler registered and turn real 404s into this page.
func TestOnlyMatchesRoot(t *testing.T) {
	if w := get(newTestMux(t, true, nil), "/no-such-page"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestUnbindRemovesMachineAndRedirects(t *testing.T) {
	reg := &fakeMachines{machines: []Machine{{
		ID:        "mch-1",
		Name:      "prod vm",
		PublicURL: "https://vm.example.com",
		CreatedAt: time.Now().UTC(),
	}}}
	mux := newTestMux(t, true, reg)

	form := url.Values{"machine_id": {"mch-1"}}
	req := httptest.NewRequest(http.MethodPost, "/machines/unbind", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusSeeOther, w.Body)
	}
	if got := w.Header().Get("Location"); got != "/?unbound=1" {
		t.Fatalf("Location = %q, want /?unbound=1", got)
	}
	listed, _ := reg.ListMachines(context.Background(), "account-1")
	if len(listed) != 0 {
		t.Fatalf("machines left after unbind: %+v", listed)
	}

	// Flash on the following GET.
	home := get(mux, "/?unbound=1")
	if !strings.Contains(home.Body.String(), "Machine unbound") {
		t.Fatalf("flash missing after unbind:\n%s", home.Body.String())
	}
}

func TestUnbindRejectsCrossOrigin(t *testing.T) {
	reg := &fakeMachines{machines: []Machine{{ID: "mch-1", Name: "prod", PublicURL: "https://vm.example.com"}}}
	mux := newTestMux(t, true, reg)

	form := url.Values{"machine_id": {"mch-1"}}
	req := httptest.NewRequest(http.MethodPost, "/machines/unbind", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	listed, _ := reg.ListMachines(context.Background(), "account-1")
	if len(listed) != 1 {
		t.Fatalf("cross-origin POST must not unbind; machines = %+v", listed)
	}
}

func TestUnbindUnknownMachine(t *testing.T) {
	mux := newTestMux(t, true, &fakeMachines{})
	form := url.Values{"machine_id": {"nope"}}
	req := httptest.NewRequest(http.MethodPost, "/machines/unbind", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not on this account") {
		t.Fatalf("error copy missing:\n%s", w.Body.String())
	}
}
