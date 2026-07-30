// Package device implements the RFC 8628 device authorization flow that
// `ao setup-vm` uses to bind a VM to an account, the two browser pages that
// flow goes through, the machine registration the approval performs, and the
// authenticated list-machines API the desktop reads.
//
// The one thing to get right here: the access tokens this package mints carry
// `aud` = machines.id, never the machine's hostname and never its public URL.
// See TOKEN_CONTRACT.md. Minting goes through tokens.Issuer, which already
// encodes that; nothing in this package constructs a JWT itself.
package device

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

//go:embed templates/*.html
var templatesFS embed.FS

const (
	// deviceCodeTTL is how long a device code stays pollable and approvable.
	// RFC 8628 has no required value; 15 minutes is long enough for a human to
	// walk to a browser and sign in, and short enough that an unapproved code
	// left in a terminal scrollback stops being useful quickly.
	deviceCodeTTL = 15 * time.Minute

	// pollInterval is the `interval` handed to the polling client and the
	// minimum spacing this server enforces between two polls of the same
	// device code. A client that polls faster gets `slow_down`.
	pollInterval = 5 * time.Second

	// verificationPath is the page the user is told to open. It is the path
	// component of the `verification_uri` in the device authorization
	// response, and the spec's end-to-end flow names it explicitly.
	verificationPath = "/device"
)

// sessions is the slice of the auth service this package needs: who is signed
// in on the current browser request. It is an interface so the device flow
// does not import the whole auth service, and so tests can sign a request in
// without building an OAuth client.
type sessions interface {
	AccountFromRequest(r *http.Request) (accountID string, ok bool)
}

// Service owns the device flow endpoints, the two browser pages, and the
// machine registry API.
type Service struct {
	db       *sql.DB
	issuer   *tokens.Issuer
	sessions sessions

	// publicOrigin is this control plane's origin, already stripped of any
	// trailing slash by config.Load. It builds the verification URI the VM
	// prints.
	publicOrigin string

	tmpl     *template.Template
	attempts *attemptLimiter
}

// NewService builds the device flow Service. issuer mints the access token
// returned to a polling client once its code is approved; sessions identifies
// the signed-in operator on the browser pages.
func NewService(db *sql.DB, issuer *tokens.Issuer, s sessions, publicOrigin string) (*Service, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse device templates: %w", err)
	}
	return &Service{
		db:           db,
		issuer:       issuer,
		sessions:     s,
		publicOrigin: publicOrigin,
		tmpl:         tmpl,
		attempts:     newAttemptLimiter(),
	}, nil
}

// Register wires every route this package owns onto mux. This is the one call
// the rest of the control plane needs to know about, so the device flow can be
// developed and merged alongside the other control plane work without either
// touching the other's files.
func (s *Service) Register(mux *http.ServeMux) {
	// RFC 8628 endpoints, called by ao setup-vm.
	mux.HandleFunc("POST /device/code", s.handleDeviceCode)
	mux.HandleFunc("POST /device/token", s.handleDeviceToken)

	// The two browser pages, reached from the signed-in session.
	mux.HandleFunc("GET /device", s.handleEnterCodePage)
	mux.HandleFunc("POST /device", s.handleSubmitCode)
	mux.HandleFunc("POST /device/decision", s.handleDecision)

	// The machine registry API the desktop reads.
	mux.HandleFunc("GET /api/v1/machines", s.handleListMachines)
}

// verificationURI is the absolute URL of the enter-code page.
func (s *Service) verificationURI() string {
	return s.publicOrigin + verificationPath
}
