package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Tracker is the outbound port for issue trackers (GitHub Issues, GitLab
// Issues, Linear). v1 is read-only: Get returns a normalized snapshot used
// by spawn-bootstrap to hydrate the agent prompt. Mirroring agent lifecycle
// back onto the tracker (Comment, Transition) is deferred to issue #40, and
// the observer/polling loop is deferred to issue #35.
//
// All v1 providers share this interface. Provider differences (label vs
// state machine vs close reason) are absorbed inside each adapter via
// domain.NormalizedIssueState. Fields on domain.Issue exist only when every
// provider can populate them; richer per-provider metadata belongs behind a
// separate port.
type Tracker interface {
	Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error)
}
