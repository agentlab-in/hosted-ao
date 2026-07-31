// Package desktopauth implements the desktop app's sign-in: the RFC 8252
// loopback authorization-code exchange with PKCE that
// docs/desktop-login-contract.md specifies and frontend/src/main/ao-pkce.ts
// already calls. GET /oauth/desktop/authorize drives the browser through the
// auth package's existing Google session and hands a one-time code back to the
// app's loopback listener; POST /oauth/desktop/token trades that code for the
// account and a new refresh token.
//
// This is the control plane's only public, unauthenticated entry point that
// ends in a ninety-day credential, so three things carry the whole flow and
// none of them may be relaxed:
//
//   - The redirect target is proven to be a loopback address before it is ever
//     used, including to report an error. Any other host would make this an
//     open redirect that hands out accounts.
//   - The code is bound at issue time to the PKCE challenge, the redirect URI,
//     and the account, and all three are re-checked at the token endpoint
//     against the stored row, never against the token request.
//   - The code is deleted in the same transaction that inserts the refresh
//     token, so no interleaving of a replay can mint a second one.
//
// Nothing here logs a code, a verifier, or a token.
package desktopauth

import (
	"database/sql"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

const (
	// desktopClientID is the one client this flow serves. The desktop app is a
	// public client, so this identifies it and authenticates nothing; PKCE does
	// the work a client secret would.
	desktopClientID = "ao-desktop"

	// authCodeTTL bounds how long an issued code stays redeemable. The desktop
	// exchanges it the instant its loopback listener receives the redirect, so
	// this only has to cover a slow hop through the browser. RFC 6749 section
	// 4.1.2 recommends a maximum of ten minutes; half of that is plenty and
	// halves the window in which a leaked code is worth anything.
	authCodeTTL = 5 * time.Minute

	// googleLoginPath is the auth package's existing Google sign-in entry
	// point. The authorize endpoint sends a signed-out browser through it with
	// ?next= pointing back at itself, so there is one Google flow and one
	// session cookie in this service, not two.
	googleLoginPath = "/auth/google/login"
)

// sessions is the slice of the auth service this package needs: who is signed
// in on the current browser request. An interface so the login exchange does
// not import the whole auth service, and so tests can sign a request in
// without standing up an OAuth client.
type sessions interface {
	AccountFromRequest(r *http.Request) (accountID string, ok bool)
}

// Service owns the two desktop login endpoints.
type Service struct {
	db       *sql.DB
	issuer   *tokens.Issuer
	sessions sessions
	limiter  *windowLimiter
}

// NewService builds the desktop login Service. issuer mints the refresh token
// the exchange returns; s identifies the operator signed in on the browser
// request that reaches the authorize endpoint.
func NewService(db *sql.DB, issuer *tokens.Issuer, s sessions) *Service {
	return &Service{db: db, issuer: issuer, sessions: s, limiter: newWindowLimiter()}
}

// Register wires this package's two routes onto mux. One call, so this flow
// merges alongside the rest of the control plane without either touching the
// other's files.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/desktop/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/desktop/token", s.handleToken)
}

const (
	// tokenWindow and tokenAttemptsPerWindow bound how fast one caller may hit
	// the token endpoint. Guessing a code is not the threat that motivates this
	// (a code is 256 bits and lives five minutes); the endpoint opens a write
	// transaction per call while unauthenticated, so the point is to keep one
	// caller from making that everybody's problem. The allowance is well past
	// what a human retrying a sign-in produces.
	tokenWindow            = time.Minute
	tokenAttemptsPerWindow = 20
)

// windowLimiter is a fixed-window counter keyed by caller.
//
// The whole map is dropped at each window boundary rather than swept per key,
// so memory is bounded by the distinct callers seen inside one window and an
// attacker cycling addresses cannot grow it without bound. That costs a caller
// straddling a boundary a fresh allowance, which for a limit this coarse is not
// worth a sliding window.
//
// ponytail: in-memory, so it bounds one process. The control plane is a single
// instance today (one Caddy site on one box); if it is ever replicated, move
// this to a shared store or each replica grants the full allowance.
type windowLimiter struct {
	mu     sync.Mutex
	start  time.Time
	counts map[string]int
	clock  func() time.Time
}

func newWindowLimiter() *windowLimiter {
	return &windowLimiter{counts: make(map[string]int), clock: time.Now}
}

// allow records an attempt for key and reports whether it is within the
// window's allowance.
func (l *windowLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	if now.Sub(l.start) >= tokenWindow {
		l.start = now
		l.counts = make(map[string]int)
	}
	l.counts[key]++
	return l.counts[key] <= tokenAttemptsPerWindow
}

// clientKey identifies the caller for rate limiting.
//
// The control plane sits behind Caddy on the same box, so every request's
// RemoteAddr is the loopback and keying on it alone would collapse the whole
// internet into one bucket: one caller could then spend everybody's allowance.
// When the immediate peer is that loopback proxy, the last X-Forwarded-For hop
// is used instead, because Caddy appends the address it actually saw to
// whatever the client sent. The last entry is therefore the one value in that
// header a client cannot forge; the earlier ones are attacker text and are
// ignored. When the peer is not the loopback there is no proxy in front, so
// the header is ignored entirely.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return host
	}
	fwd := r.Header.Get("X-Forwarded-For")
	if i := strings.LastIndex(fwd, ","); i >= 0 {
		fwd = fwd[i+1:]
	}
	if fwd = strings.TrimSpace(fwd); fwd != "" {
		return fwd
	}
	return host
}
