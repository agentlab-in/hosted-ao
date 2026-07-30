// Package api owns the control plane's own HTTP API surface: the token
// endpoint a desktop install exchanges its refresh token at, the bearer
// authentication every /api/v1 route uses, and the JSON and OAuth error
// conventions those routes share.
//
// The credential for an /api/v1 route is an access token whose `aud` is this
// control plane's origin, and nothing else. In particular a refresh token is
// presented only here, at the token endpoint, where it is exchanged and
// rotated; it is never accepted on a resource route, where it would travel on
// every call and turn one leaked log line into ninety days of account access.
// See TOKEN_CONTRACT.md, "The two audiences".
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
)

// refreshGrantType is the OAuth grant type for exchanging a refresh token.
const refreshGrantType = "refresh_token"

// Service holds what the control plane API needs: the issuer that mints and
// verifies control-plane-audience access tokens and rotates refresh tokens.
type Service struct {
	issuer *tokens.Issuer
}

// NewService builds the API service.
func NewService(issuer *tokens.Issuer) *Service {
	return &Service{issuer: issuer}
}

// Register wires the token endpoint onto mux. Resource routes live with the
// feature that owns them and authenticate through Authenticate.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/token", s.handleToken)
}

// tokenResponse is the token endpoint's success body. The refresh token
// rotates on every use per TOKEN_CONTRACT.md, so the caller must persist the
// replacement it gets back here: the one it presented is already revoked.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// handleToken exchanges a refresh token for a fresh control-plane-audience
// access token and a rotated refresh token.
//
// The access token it returns cannot be used against a VM: its `aud` is this
// control plane's origin, and a gateway requires its own machine id. A token
// for a machine is obtained from that machine's own binding, not here.
func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	params, err := ReadParams(w, r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "could not parse the request body")
		return
	}
	if gt := params.Get("grant_type"); gt != "" && gt != refreshGrantType {
		WriteError(w, http.StatusBadRequest, "unsupported_grant_type", "expected grant_type "+refreshGrantType)
		return
	}
	presented := strings.TrimSpace(params.Get("refresh_token"))
	if presented == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	rotated, accountID, _, err := s.issuer.RotateRefreshToken(r.Context(), presented)
	if err != nil {
		switch {
		case errors.Is(err, tokens.ErrInvalidRefreshToken),
			errors.Is(err, tokens.ErrRefreshTokenRevoked),
			errors.Is(err, tokens.ErrRefreshTokenExpired):
			// One error for all three: a caller must not be able to tell an
			// unknown token from a revoked one, which would make this a
			// probe for which tokens ever existed. A replayed token lands
			// here, because rotation revoked it when it was first used.
			WriteError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is not valid, sign in again")
		default:
			log.Printf("api: rotate refresh token: %v", err)
			WriteError(w, http.StatusInternalServerError, "server_error", "could not exchange the refresh token")
		}
		return
	}

	accessToken, err := s.issuer.IssueControlPlaneToken(accountID)
	if err != nil {
		log.Printf("api: issue control plane token: %v", err)
		WriteError(w, http.StatusInternalServerError, "server_error", "could not issue an access token")
		return
	}

	WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.issuer.AccessTokenTTL().Seconds()),
		RefreshToken: rotated,
	})
}

// Authenticate resolves the account behind a control plane API request from
// its bearer token, or reports false.
//
// It is the authenticator every /api/v1 route is wired with, passed as a
// value rather than reached for directly, so the credential this service
// accepts is one substitution rather than an edit in every feature package.
func (s *Service) Authenticate(r *http.Request) (string, bool) {
	token, ok := bearerToken(r)
	if !ok {
		return "", false
	}
	accountID, err := s.issuer.VerifyControlPlaneToken(token)
	if err != nil {
		return "", false
	}
	return accountID, true
}

// bearerToken extracts the credential from an Authorization header. The
// scheme is compared case-insensitively because RFC 7235 says it is.
func bearerToken(r *http.Request) (string, bool) {
	scheme, value, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// Unauthorized writes the 401 every API route returns for a missing, expired,
// wrongly addressed, or unverifiable credential. The reason is deliberately
// not distinguished to the caller.
func Unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="ao", error="invalid_token"`)
	WriteError(w, http.StatusUnauthorized, "invalid_token", "a valid access token is required")
}

// ReadParams accepts either a form-encoded body, which is what the OAuth
// specs use, or a JSON object, which is what a Go or Electron client is more
// likely to send. Both are read into url.Values so handlers do not care which
// arrived. A JSON value that is not a string is ignored rather than
// stringified, so a client cannot smuggle a number or an object into a field.
func ReadParams(w http.ResponseWriter, r *http.Request) (url.Values, error) {
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var raw map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&raw); err != nil {
			return nil, err
		}
		values := url.Values{}
		for k, v := range raw {
			if str, ok := v.(string); ok {
				values.Set(k, str)
			}
		}
		return values, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.PostForm, nil
}

// WriteJSON writes a JSON body with the no-store the control plane's
// responses need: they carry bearer secrets, so nothing between here and the
// client may keep a copy.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errorResponse is the OAuth error shape every endpoint here returns.
type errorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// WriteError writes an OAuth-shaped error body.
func WriteError(w http.ResponseWriter, status int, code, description string) {
	WriteJSON(w, status, errorResponse{Error: code, ErrorDescription: description})
}
