package auth

import (
	"embed"
	"log"
	"net/http"
	"net/url"
)

//go:embed templates/*.html
var templatesFS embed.FS

// signInPageData is the html/template data for templates/signin.html.
type signInPageData struct {
	LoginURL string
	Error    string
}

func (s *Service) handleSignInPage(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))

	loginURL := "/auth/google/login"
	if next != "/" {
		loginURL += "?next=" + url.QueryEscape(next)
	}

	data := signInPageData{
		LoginURL: loginURL,
		Error:    errorMessage(r.URL.Query().Get("error")),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "signin.html", data); err != nil {
		log.Printf("auth: render sign-in page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// errorMessage maps the ?error= codes the OAuth handlers redirect back to
// the sign-in page with into operator-facing text.
func errorMessage(code string) string {
	switch code {
	case "":
		return ""
	case "access_denied":
		return "Sign-in was cancelled."
	case "expired", "state_mismatch":
		return "Sign-in could not be verified. Please try again."
	default:
		return "Sign-in failed. Please try again."
	}
}
