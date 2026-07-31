// Package home serves the control plane's landing page at "/".
//
// It exists because sign-in has to land somewhere. Google returns the operator
// to /auth/google/callback, which redirects to the flow's `next`, and that
// defaults to "/": with nothing registered there, a successful sign-in ended
// on a 404, which reads as a broken service at the exact moment the operator
// has just proved it is not.
//
// It is deliberately not a dashboard. The control plane's job is binding a
// machine to an account, so the page says what to do next and links to the one
// page that does it.
package home

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates/*.html
var templatesFS embed.FS

// sessions is the slice of the auth service this package needs: who is signed
// in on the current browser request. An interface, for the same reason the
// device flow uses one, so the landing page does not pull in the OAuth client.
type sessions interface {
	AccountFromRequest(r *http.Request) (accountID string, ok bool)
}

// Service serves the landing page.
type Service struct {
	sessions sessions
	tmpl     *template.Template
}

// NewService builds the landing page service.
func NewService(s sessions) (*Service, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse home templates: %w", err)
	}
	return &Service{sessions: s, tmpl: tmpl}, nil
}

// Register wires the landing page onto mux. This is the one call the rest of
// the control plane needs to know about.
//
// The pattern is "/{$}", which matches the root path exactly. A bare "/" would
// match every path no other handler claims, turning every genuine 404 into
// this page.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleHome)
}

// handleHome renders the landing page for a signed-in operator, and sends
// everyone else to sign in.
//
// The unauthenticated case is a redirect to /login, not a 404 and not a public
// page: "/" is the address a person types, the only thing they can usefully do
// here needs an account, and /login already sends them back to "/" afterwards
// because that is its default `next`.
func (s *Service) handleHome(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessions.AccountFromRequest(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "home.html", nil); err != nil {
		log.Printf("home: render landing page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
