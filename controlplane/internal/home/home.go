// Package home serves the control plane's account landing page at "/".
//
// After sign-in the operator lands here. The page lists machines bound to the
// account, lets them unbind one, and points at the device flow and setup-vm
// when the list is empty. It is not a second AO board: sessions and terminals
// stay on each machine's daemon.
package home

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

// sessions is the slice of the auth service this package needs: who is signed
// in on the current browser request.
type sessions interface {
	AccountFromRequest(r *http.Request) (accountID string, ok bool)
}

// Machine is one registered VM shown on the account home page.
type Machine struct {
	ID        string
	Name      string
	PublicURL string
	CreatedAt time.Time
	LastSeen  *time.Time
}

// Machines is the registry this page reads and mutates. device.Service is the
// production implementation.
type Machines interface {
	ListMachines(ctx context.Context, accountID string) ([]Machine, error)
	RevokeMachine(ctx context.Context, accountID, machineID string, now time.Time) (bool, error)
}

// Service serves the account home page.
type Service struct {
	sessions     sessions
	machines     Machines
	publicOrigin string
	tmpl         *template.Template
}

// NewService builds the account home page service.
func NewService(s sessions, m Machines, publicOrigin string) (*Service, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"formatTime": formatTime,
		"formatSeen": formatSeen,
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse home templates: %w", err)
	}
	return &Service{
		sessions:     s,
		machines:     m,
		publicOrigin: publicOrigin,
		tmpl:         tmpl,
	}, nil
}

// Register wires the account home routes onto mux.
//
// GET /{$} matches the root path exactly. A bare "/" would claim every path no
// other handler registered and turn real 404s into this page.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("POST /machines/unbind", s.handleUnbind)
}

type pageData struct {
	Machines []Machine
	Flash    string
	Error    string
}

func (s *Service) handleHome(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.sessions.AccountFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	machines, err := s.machines.ListMachines(r.Context(), accountID)
	if err != nil {
		log.Printf("home: list machines: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	flash := r.URL.Query().Get("unbound")
	s.render(w, http.StatusOK, pageData{
		Machines: machines,
		Flash:    flash,
	})
}

// handleUnbind revokes a machine from the signed-in account via a browser form.
//
// Session cookie plus same-origin check (same pattern as the device approval
// POSTs). On success redirects to / so a refresh does not re-POST.
func (s *Service) handleUnbind(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.sessions.AccountFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !s.sameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	machineID := r.FormValue("machine_id")
	if machineID == "" {
		http.Error(w, "machine_id is required", http.StatusBadRequest)
		return
	}

	revoked, err := s.machines.RevokeMachine(r.Context(), accountID, machineID, time.Now().UTC())
	if err != nil {
		log.Printf("home: unbind machine: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !revoked {
		// Same soft landing as an already-revoked row: re-render the list.
		machines, listErr := s.machines.ListMachines(r.Context(), accountID)
		if listErr != nil {
			log.Printf("home: list machines after failed unbind: %v", listErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.render(w, http.StatusNotFound, pageData{
			Machines: machines,
			Error:    "That machine is not on this account (it may already be unbound).",
		})
		return
	}

	http.Redirect(w, r, "/?unbound=1", http.StatusSeeOther)
}

// sameOrigin rejects a browser POST that did not come from this control plane.
// Missing Origin is allowed (curl, tests); a present wrong Origin is not.
func (s *Service) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != s.publicOrigin {
		http.Error(w, "That request did not come from this page.", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Service) render(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "home.html", data); err != nil {
		log.Printf("home: render landing page: %v", err)
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func formatSeen(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
