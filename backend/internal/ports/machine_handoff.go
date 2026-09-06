package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// MachineHandoffStore uses revision-based compare-and-swap for every state
// change. Creation retries must match the immutable request identity.
type MachineHandoffStore interface {
	CreateMachineHandoff(context.Context, domain.MachineHandoff) (domain.MachineHandoff, bool, error)
	GetMachineHandoff(context.Context, string, domain.HandoffRole) (domain.MachineHandoff, bool, error)
	AdvanceMachineHandoff(context.Context, domain.MachineHandoff, domain.HandoffState, int64) (bool, error)
	MachineHandoffFencesSession(context.Context, domain.SessionID) (bool, error)
}
