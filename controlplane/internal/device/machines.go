package device

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
)

// machinesResponse is the list-machines body. It is an object rather than a
// bare array so a later addition (paging, a revoked list) does not have to
// break the shape.
type machinesResponse struct {
	Machines []Machine `json:"machines"`
}

// handleListMachines serves the account's registered machines to the desktop.
//
// The credential is an access token whose `aud` is the control plane's
// origin, resolved by the injected authenticator. A machine-audience token is
// rejected here, and a refresh token is not a credential for this route at
// all: it goes only to the token endpoint, which exchanges and rotates it.
func (s *Service) handleListMachines(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.apiAuth(r)
	if !ok {
		api.Unauthorized(w)
		return
	}

	machines, err := s.ListMachines(r.Context(), accountID)
	if err != nil {
		log.Printf("device: list machines: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not list machines")
		return
	}
	api.WriteJSON(w, http.StatusOK, machinesResponse{Machines: machines})
}

// machineTokenResponse is the machine-token endpoint's success body. It is the
// control plane token endpoint's shape minus the refresh token, which does not
// belong here: nothing rotates on this call.
//
// The machine id is not echoed back. The caller put it in the path, and it is
// the `aud` of the token in this body.
type machineTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// handleMachineToken mints an access token addressed to one of the calling
// account's machines, so the desktop can call that machine's gateway.
//
// The credential for this route is the same control-plane-audience access
// token every other /api/v1 route takes, not the refresh token: a machine
// token is wanted whenever the desktop switches machines or a 15 minute token
// lapses, and putting that on the refresh token would rotate a 90 day
// credential on every one of those calls.
//
// The token comes out of the same Issuer.IssueAccessToken the device flow's
// approval used to call, so `aud` is machines.id, never the hostname or the
// public URL, and `sub` is the account id.
func (s *Service) handleMachineToken(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.apiAuth(r)
	if !ok {
		api.Unauthorized(w)
		return
	}

	machineID := r.PathValue("id")
	owned, err := s.ownsMachine(r.Context(), accountID, machineID)
	if err != nil {
		log.Printf("device: look up machine for token: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not issue an access token")
		return
	}
	if !owned {
		// One answer for "no such machine", "someone else's machine", and
		// "revoked". Anything that distinguished them would turn a signed-in
		// account into an oracle for which machine ids exist and who owns
		// them, over a namespace the caller can enumerate.
		notFound(w)
		return
	}

	accessToken, err := s.issuer.IssueAccessToken(accountID, machineID)
	if err != nil {
		log.Printf("device: issue machine access token: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not issue an access token")
		return
	}

	api.WriteJSON(w, http.StatusOK, machineTokenResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int(s.issuer.AccessTokenTTL().Seconds()),
	})
}

// handleRevokeMachine unbinds a machine from the calling account.
//
// Sets revoked_at; the row stays as a tombstone so history is not rewritten.
// Same not-found story as the token endpoint for foreign, missing, and already
// revoked ids.
func (s *Service) handleRevokeMachine(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.apiAuth(r)
	if !ok {
		api.Unauthorized(w)
		return
	}

	machineID := r.PathValue("id")
	revoked, err := s.RevokeMachine(r.Context(), accountID, machineID, time.Now().UTC())
	if err != nil {
		log.Printf("device: revoke machine: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not unbind the machine")
		return
	}
	if !revoked {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notFound is the answer to a machine id this account may not have a token
// for, whatever the reason.
func notFound(w http.ResponseWriter) {
	api.WriteError(w, http.StatusNotFound, "not_found", "no such machine")
}

// ownsMachine reports whether machineID is one of accountID's live machines.
//
// Both conditions are in one query on purpose. Reading the row and then
// comparing account_id in Go would take a visibly different path for a machine
// that does not exist than for one owned by someone else, and this endpoint's
// whole rejection story is that those two are the same answer. One indexed
// primary key lookup either matches every condition or matches nothing.
func (s *Service) ownsMachine(ctx context.Context, accountID, machineID string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM machines
		  WHERE id = ? AND account_id = ? AND revoked_at IS NULL`,
		machineID, accountID,
	).Scan(&found)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("look up machine %q: %w", machineID, err)
	}
}
