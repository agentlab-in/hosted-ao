package auth

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServiceWithTemplate(t *testing.T) *Service {
	t.Helper()
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return &Service{tmpl: tmpl}
}

func TestHandleSignInPage_RendersGoogleLoginLink(t *testing.T) {
	s := newTestServiceWithTemplate(t)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	s.handleSignInPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/auth/google/login"`) {
		t.Errorf("body missing google login link, got: %s", body)
	}
	if strings.Contains(body, `class="error"`) {
		t.Error("body has an error message with no ?error= param")
	}
}

func TestHandleSignInPage_ForwardsNextAndRendersError(t *testing.T) {
	s := newTestServiceWithTemplate(t)

	req := httptest.NewRequest(http.MethodGet, "/login?next=%2Fdevice&error=access_denied", nil)
	rec := httptest.NewRecorder()
	s.handleSignInPage(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `href="/auth/google/login?next=%2Fdevice"`) {
		t.Errorf("body missing next-forwarding login link, got: %s", body)
	}
	if !strings.Contains(body, "Sign-in was cancelled.") {
		t.Errorf("body missing access_denied error message, got: %s", body)
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"", ""},
		{"access_denied", "Sign-in was cancelled."},
		{"expired", "Sign-in could not be verified. Please try again."},
		{"state_mismatch", "Sign-in could not be verified. Please try again."},
		{"something_else", "Sign-in failed. Please try again."},
	}
	for _, tt := range tests {
		if got := errorMessage(tt.code); got != tt.want {
			t.Errorf("errorMessage(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
