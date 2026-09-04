package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// MobileAPIVersion is the contract version the phone negotiates against. Bump
// it when a change to the mobile-facing surface is not backward compatible.
const MobileAPIVersion = 1

// IdentityController returns an opaque host ID and API version. HAO serves it
// behind the LAN password and gateway authentication; loopback remains open.
type IdentityController struct {
	HostID string
}

// Register mounts the identity route on the supplied router.
func (c *IdentityController) Register(r chi.Router) {
	r.Get("/identity", c.identity)
}

func (c *IdentityController) identity(w http.ResponseWriter, r *http.Request) {
	if c.HostID == "" {
		apispec.NotImplemented(w, r, "GET", "/api/v1/identity")
		return
	}
	envelope.WriteJSON(w, http.StatusOK, IdentityResponse{
		HostID:     c.HostID,
		APIVersion: MobileAPIVersion,
	})
}
