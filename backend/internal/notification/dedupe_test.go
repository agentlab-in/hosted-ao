package notification

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDedupeSameReactionConditionProducesSameKey(t *testing.T) {
	rec := dedupeRecord("working", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	e := ports.Event{SessionID: "ao-1", Reaction: &ports.ReactionEvent{Key: "agent-needs-input", Action: "notify"}}

	k1 := ComputeDedupeKey(e, rec, domain.PRFacts{})
	k2 := ComputeDedupeKey(e, rec, domain.PRFacts{})
	if k1 != k2 {
		t.Fatalf("dedupe key unstable: %q != %q", k1, k2)
	}
}

func TestDedupeChangedConditionProducesNewKey(t *testing.T) {
	e := ports.Event{SessionID: "ao-1", Reaction: &ports.ReactionEvent{Key: "agent-needs-input", Action: "notify"}}
	r1 := dedupeRecord("needs_input", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	r2 := dedupeRecord("needs_input", time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC))

	if ComputeDedupeKey(e, r1, domain.PRFacts{}) == ComputeDedupeKey(e, r2, domain.PRFacts{}) {
		t.Fatal("changed session updated timestamp should change dedupe key")
	}
}

func TestDedupeEscalationIncludesCauseAndDoesNotCollideWithBase(t *testing.T) {
	rec := dedupeRecord("working", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	base := ports.Event{SessionID: "ao-1", Reaction: &ports.ReactionEvent{Key: "ci-failed", Action: "notify"}}
	esc := ports.Event{
		SessionID:  "ao-1",
		Reaction:   &ports.ReactionEvent{Key: "ci-failed", Action: "escalated"},
		Escalation: &ports.EscalationEvent{Attempts: 3, Cause: "max_retries"},
	}
	otherCause := esc
	otherCause.Escalation = &ports.EscalationEvent{Attempts: 3, Cause: "max_duration"}

	baseKey := ComputeDedupeKey(base, rec, domain.PRFacts{Exists: true, URL: "pr", CI: domain.CIFailing})
	escKey := ComputeDedupeKey(esc, rec, domain.PRFacts{Exists: true, URL: "pr", CI: domain.CIFailing})
	otherKey := ComputeDedupeKey(otherCause, rec, domain.PRFacts{Exists: true, URL: "pr", CI: domain.CIFailing})
	if baseKey == escKey {
		t.Fatal("escalation dedupe key should not collide with base reaction")
	}
	if escKey == otherKey {
		t.Fatal("escalation cause should affect dedupe key")
	}
}

func dedupeRecord(state domain.SessionState, updated time.Time) domain.SessionRecord {
	return domain.SessionRecord{
		ID:        "ao-1",
		ProjectID: "ao",
		Lifecycle: domain.CanonicalSessionLifecycle{
			Session: domain.SessionSubstate{State: state},
		},
		UpdatedAt: updated,
	}
}
