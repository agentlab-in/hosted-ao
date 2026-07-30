package device

import (
	"log"
	"net/http"
	"strings"
)

// machinesResponse is the list-machines body. It is an object rather than a
// bare array so a later addition (paging, a revoked list) does not have to
// break the shape.
type machinesResponse struct {
	Machines []machine `json:"machines"`
}

// handleListMachines serves the account's registered machines to the desktop.
//
// Credentials, in order: `Authorization: Bearer <refresh token>`, or the
// browser session cookie.
//
// The bearer credential here is the opaque refresh token from
// TOKEN_CONTRACT.md, not an access token, because the access tokens in that
// contract are addressed to one machine (`aud` = machines.id) and are meant
// for that machine's gateway. A desktop install that has just signed in has
// no machine yet, so it has no token addressed to anything; the refresh token
// is the only account-bound, revocable credential it holds. Validating it
// does not rotate it: rotation stays tied to exchanging it for an access
// token. The session cookie is also accepted, and only on this read-only
// route, so the list is verifiable from the same browser that just approved a
// machine.
func (s *Service) handleListMachines(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.accountForAPIRequest(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ao", error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, "invalid_token", "a valid credential is required")
		return
	}

	machines, err := s.listMachines(r.Context(), accountID)
	if err != nil {
		log.Printf("device: list machines: %v", err)
		writeError(w, http.StatusInternalServerError, "server_error", "could not list machines")
		return
	}
	writeJSON(w, http.StatusOK, machinesResponse{Machines: machines})
}

// accountForAPIRequest resolves the calling account from either credential,
// or reports false. It never distinguishes "no credential" from "bad
// credential" to the caller, so the endpoint cannot be used to test whether a
// given refresh token exists.
func (s *Service) accountForAPIRequest(r *http.Request) (string, bool) {
	if token, ok := bearerToken(r); ok {
		accountID, err := s.issuer.AccountForRefreshToken(r.Context(), token)
		if err != nil {
			return "", false
		}
		return accountID, true
	}
	return s.sessions.AccountFromRequest(r)
}

// bearerToken extracts the credential from an Authorization header. The
// scheme is compared case-insensitively because RFC 7235 says it is
// case-insensitive and clients do vary.
func bearerToken(r *http.Request) (string, bool) {
	raw := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(raw, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
