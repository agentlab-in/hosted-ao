package device

import (
	"log"
	"net/http"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
)

// machinesResponse is the list-machines body. It is an object rather than a
// bare array so a later addition (paging, a revoked list) does not have to
// break the shape.
type machinesResponse struct {
	Machines []machine `json:"machines"`
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

	machines, err := s.listMachines(r.Context(), accountID)
	if err != nil {
		log.Printf("device: list machines: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not list machines")
		return
	}
	api.WriteJSON(w, http.StatusOK, machinesResponse{Machines: machines})
}
