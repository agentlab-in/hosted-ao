package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Tracker is the outbound port for issue trackers (GitHub Issues, GitLab
// Issues, Linear). v1 is write-mostly: spawn-bootstrap reads with Get, the
// Session Manager posts status updates with Comment, and lifecycle
// transitions (start, hand-off-to-review, close) propagate with Transition.
// There is no observer loop yet; polling and ApplyTrackerFacts arrive with
// issue #35.
//
// All three v1 providers share this interface. Provider differences (label
// vs state machine vs close reason) are absorbed inside each adapter via
// domain.NormalizedIssueState. Fields on domain.Issue exist only when every
// provider can populate them; richer per-provider metadata belongs behind a
// separate port.
type Tracker interface {
	Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error)
	Comment(ctx context.Context, id domain.TrackerID, body string) error
	Transition(ctx context.Context, id domain.TrackerID, state domain.NormalizedIssueState) error
}
