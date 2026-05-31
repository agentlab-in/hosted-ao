package notification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ConditionHash returns a deterministic, compact hash over a condition vector.
func ConditionHash(parts ...string) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// DedupeKey returns the stable durable notification idempotency key.
func DedupeKey(projectID domain.ProjectID, sessionID domain.SessionID, reactionKey, conditionHash string) string {
	return fmt.Sprintf("v1:lifecycle:%s:%s:%s:%s", projectID, sessionID, reactionKey, conditionHash)
}

// ComputeDedupeKey derives a restart-safe dedupe key from the lifecycle event
// plus current persisted state. It avoids PR updated_at because re-polling the
// same facts after daemon restart would otherwise create duplicate notifications.
func ComputeDedupeKey(event ports.Event, rec domain.SessionRecord, pr domain.PRFacts) string {
	projectID := event.ProjectID
	if projectID == "" {
		projectID = rec.ProjectID
	}
	reactionKey := reactionKeyForEvent(event)
	condition := []string{
		"session_state", string(rec.Lifecycle.Session.State),
		"termination", string(rec.Lifecycle.TerminationReason),
		"session_updated", timeKey(rec.UpdatedAt),
	}
	if pr.Exists {
		condition = append(condition,
			"pr_url", pr.URL,
			"pr_number", fmt.Sprint(pr.Number),
			"pr_draft", fmt.Sprint(pr.Draft),
			"pr_merged", fmt.Sprint(pr.Merged),
			"pr_closed", fmt.Sprint(pr.Closed),
			"ci", string(pr.CI),
			"review", string(pr.Review),
			"mergeability", string(pr.Mergeability),
			"review_comments", fmt.Sprint(pr.ReviewComments),
		)
	}
	if event.CauseKey != "" {
		condition = append(condition, "cause_key", event.CauseKey)
	}
	if event.Escalation != nil {
		condition = append(condition, "escalation_cause", event.Escalation.Cause)
	}
	return DedupeKey(projectID, event.SessionID, reactionKey, ConditionHash(condition...))
}

func reactionKeyForEvent(event ports.Event) string {
	if event.Reaction != nil && event.Reaction.Key != "" {
		return event.Reaction.Key
	}
	return reactionKeyFromType(event.Type)
}

func timeKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
