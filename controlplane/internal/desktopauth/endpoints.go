package desktopauth

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
)

// authorizationCodeGrantType is the grant this exchange accepts, and the only
// one. It is required rather than defaulted: the one client sends it, and a
// token endpoint that guesses what an unlabelled request meant is a token
// endpoint that can be surprised.
const authorizationCodeGrantType = "authorization_code"

// handleAuthorize is the authorization endpoint the desktop app opens in the
// system browser. It validates the request, drives the browser through the
// auth package's Google sign-in if there is no session yet, and redirects back
// to the app's loopback listener with a one-time code and the app's own state.
//
// The order of the checks is the security property, not a style choice. The
// client and the redirect target are settled first, because until the redirect
// URI is proven to be loopback there is no address this endpoint may send
// anything to, not a code and not an error either. RFC 6749 section 4.1.2.1
// says exactly that: with an invalid client or redirect URI, inform the user
// and do not redirect. Everything after that point can be reported to the app,
// because by then the only place a report can go is the app's own machine.
//
// One residual risk this shape carries, from the contract rather than from
// this code: a browser that already holds a session completes the flow with no
// interaction, so any local process that can open a URL can run its own
// authorization request against the signed-in operator's browser and receive a
// code on its own loopback port. RFC 8252 section 8.10 treats that as inherent
// to native app loopback flows, PKCE does not help because such a caller picks
// its own verifier, and the defence is that the process is already running as
// the user. Adding a confirmation step would change the documented contract
// the desktop is built against, so it is not done here.
func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if q.Get("client_id") != desktopClientID {
		refuse(w, "This sign-in link is not for the AO desktop app.")
		return
	}
	redirectURI, err := validateLoopbackRedirect(q.Get("redirect_uri"))
	if err != nil {
		// The reason is safe to show: it is a property of the link the user
		// followed, not of any account, and a desktop build with the wrong
		// redirect URI is otherwise a silent hang.
		refuse(w, "This sign-in link cannot be completed: "+err.Error()+
			". The AO desktop app receives sign-in on a loopback address on this computer, and nowhere else.")
		return
	}
	state := q.Get("state")
	if !validState(state) {
		// Also not redirected, even though the target is now proven. The app
		// checks state before it reads anything else out of the callback, so a
		// redirect carrying no state is one it would ignore, leaving the user
		// with a browser that did nothing and a listener that times out.
		refuse(w, "This sign-in link is missing the value that ties it to the app's request, so it cannot be completed.")
		return
	}

	if rt := q.Get("response_type"); rt != "code" {
		redirectError(w, r, redirectURI, state, "unsupported_response_type", "this endpoint issues authorization codes only")
		return
	}
	if m := q.Get("code_challenge_method"); m != "S256" {
		redirectError(w, r, redirectURI, state, "invalid_request", "code_challenge_method must be S256")
		return
	}
	challenge := q.Get("code_challenge")
	if !validS256Challenge(challenge) {
		redirectError(w, r, redirectURI, state, "invalid_request", "code_challenge must be the base64url SHA-256 of the code verifier")
		return
	}

	accountID, ok := s.sessions.AccountFromRequest(r)
	if !ok {
		// Back here once Google has signed the operator in, with the request
		// intact. RequestURI is this endpoint's own path and query, so it
		// survives the auth package's same-site check on ?next=.
		http.Redirect(w, r, googleLoginPath+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}

	code, err := s.createCode(r.Context(), accountID, redirectURI, challenge, time.Now().UTC())
	if err != nil {
		log.Printf("desktopauth: issue authorization code: %v", err)
		redirectError(w, r, redirectURI, state, "server_error", "the control plane could not issue an authorization code")
		return
	}
	redirectBack(w, r, redirectURI, url.Values{"code": {code}, "state": {state}})
}

// tokenResponse is the exchange's success body, exactly as
// docs/desktop-login-contract.md specifies it: identity plus the refresh
// token, and no access token. Every access token comes from a later exchange
// at POST /api/v1/token.
type tokenResponse struct {
	RefreshToken string         `json:"refresh_token"`
	Account      accountSummary `json:"account"`
}

type accountSummary struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// handleToken exchanges an authorization code for the account and a new
// refresh token.
//
// Every way the exchange itself can fail collapses into one invalid_grant with
// one description, following the precedent api.handleToken sets for refresh
// tokens: an unknown code, an expired one, a replayed one, a mismatched
// redirect URI, and a wrong verifier are indistinguishable from out here. The
// distinctions are exactly the oracle an attacker holding a stolen code would
// want, and the app has nothing useful to do with any of them.
func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientKey(r)) {
		api.WriteError(w, http.StatusTooManyRequests, "slow_down", "too many token requests, try again shortly")
		return
	}

	params, err := api.ReadParams(w, r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_request", "could not parse the request body")
		return
	}
	if params.Get("grant_type") != authorizationCodeGrantType {
		api.WriteError(w, http.StatusBadRequest, "unsupported_grant_type", "expected grant_type "+authorizationCodeGrantType)
		return
	}
	if params.Get("client_id") != desktopClientID {
		api.WriteError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	}
	code := strings.TrimSpace(params.Get("code"))
	verifier := strings.TrimSpace(params.Get("code_verifier"))
	redirectURI := strings.TrimSpace(params.Get("redirect_uri"))
	if code == "" || verifier == "" || redirectURI == "" {
		api.WriteError(w, http.StatusBadRequest, "invalid_request", "code, code_verifier, and redirect_uri are all required")
		return
	}

	res, err := s.redeem(r.Context(), code, verifier, redirectURI, time.Now().UTC())
	if err != nil {
		if errors.Is(err, errInvalidCode) {
			api.WriteError(w, http.StatusBadRequest, "invalid_grant", "the authorization code is not valid, sign in again")
			return
		}
		log.Printf("desktopauth: redeem authorization code: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not complete the sign-in")
		return
	}

	api.WriteJSON(w, http.StatusOK, tokenResponse{
		RefreshToken: res.RefreshToken,
		Account:      accountSummary{ID: res.AccountID, Email: res.Email},
	})
}

// redirectBack sends the browser to the app's loopback listener.
//
// validateLoopbackRedirect has already refused a redirect URI carrying a query
// or a fragment, so appending "?" here cannot collide with one. no-store
// because the Location this writes carries the authorization code.
func redirectBack(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, redirectURI+"?"+params.Encode(), http.StatusFound)
}

// redirectError reports a failure to the app through its loopback listener,
// carrying the state so the app can tell this is the answer to its own
// request rather than a stray hit on its port.
func redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	redirectBack(w, r, redirectURI, url.Values{
		"error":             {code},
		"error_description": {description},
		"state":             {state},
	})
}

// refuse ends an authorization request that cannot be reported to the app,
// because the client or the redirect target is not one this endpoint will use.
// The operator is looking at this in a browser, so it is plain text they can
// read, not the JSON error envelope the token endpoint returns.
func refuse(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(message + "\n"))
}
