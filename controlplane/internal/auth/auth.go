// Package auth implements Google sign-in for the control plane: the
// authorization-code exchange with PKCE, the accounts upsert keyed on the
// Google subject, and a signed browser session cookie for the operator. It
// owns its own routes and storage access and does not reach into the shared
// server or config packages beyond reading Config, so it can be developed
// and merged independently of the keys-and-tokens work landing in the same
// tree.
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/config"
)

// Service holds everything the Google login flow needs: the database for the
// accounts upsert, the OAuth client configuration, the HTTP client used to
// talk to Google, the session-signing key, and the parsed sign-in page
// template.
type Service struct {
	db           *sql.DB
	clientID     string
	clientSecret string
	redirectURI  string
	endpoints    googleEndpoints
	httpClient   *http.Client

	sessionKey    []byte
	secureCookies bool

	tmpl *template.Template
}

// NewService builds the auth Service, loading (or generating, on first boot)
// the session-signing key under cfg.DataDir and parsing the sign-in page
// template. It does not talk to Google or the database beyond that.
func NewService(db *sql.DB, cfg config.Config) (*Service, error) {
	key, err := loadOrCreateSessionKey(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load session key: %w", err)
	}

	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	return &Service{
		db:           db,
		clientID:     cfg.GoogleClientID,
		clientSecret: cfg.GoogleClientSecret,
		// config.Load already strips any trailing slash from PublicOrigin, so
		// this concatenation cannot produce a double slash.
		redirectURI:   cfg.PublicOrigin + "/auth/google/callback",
		endpoints:     defaultGoogleEndpoints,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		sessionKey:    key,
		secureCookies: strings.HasPrefix(cfg.PublicOrigin, "https://"),
		tmpl:          tmpl,
	}, nil
}

// Register wires the auth routes onto mux: the sign-in page and the two
// Google OAuth endpoints. This is the one call the rest of the control plane
// needs to know about.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.handleSignInPage)
	mux.HandleFunc("GET /auth/google/login", s.handleGoogleLogin)
	mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)
}

// upsertAccount inserts a new account row for subject, or, if one already
// exists, updates its email and returns the existing id. Accounts are keyed
// on the Google subject rather than the email because emails change; the id
// returned here is stable across repeat logins.
func (s *Service) upsertAccount(ctx context.Context, subject, email string) (string, error) {
	const q = `
		INSERT INTO accounts (id, google_subject, email, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (google_subject) DO UPDATE SET email = excluded.email
		RETURNING id`

	var id string
	err := s.db.QueryRowContext(ctx, q, uuid.NewString(), subject, email, time.Now().UTC()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert account: %w", err)
	}
	return id, nil
}
