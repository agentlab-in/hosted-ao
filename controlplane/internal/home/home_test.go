package home

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSessions signs a request in, or does not.
type fakeSessions struct{ signedIn bool }

func (f fakeSessions) AccountFromRequest(*http.Request) (string, bool) {
	return "account-1", f.signedIn
}

func newTestMux(t *testing.T, signedIn bool) *http.ServeMux {
	t.Helper()
	svc, err := NewService(fakeSessions{signedIn: signedIn})
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
	w := get(newTestMux(t, true), "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	// The useful next step, which is the only reason this page exists.
	if !strings.Contains(w.Body.String(), `href="/device"`) {
		t.Fatalf("landing page does not link to /device:\n%s", w.Body.String())
	}
}

// TestUnauthenticatedRedirectsToLogin: the deliberate choice for an anonymous
// GET /. Not a 404, and not a public page, because everything reachable from
// here needs an account.
func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	w := get(newTestMux(t, false), "/")

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
	if w := get(newTestMux(t, true), "/no-such-page"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
