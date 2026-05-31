package notification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeReader struct {
	rec domain.SessionRecord
	pr  domain.PRFacts
}

func (f fakeReader) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.rec, true, nil
}
func (f fakeReader) PRFactsForSession(context.Context, domain.SessionID) (domain.PRFacts, error) {
	return f.pr, nil
}

func TestSemanticTypeMapping(t *testing.T) {
	cases := map[string]string{
		"approved-and-green": "merge.ready",
		"agent-stuck":        "session.stuck",
		"agent-needs-input":  "session.needs_input",
		"agent-exited":       "session.exited",
		"pr-closed":          "pr.closed",
		"pr-merged":          "pr.merged",
		"ci-failed":          "ci.failing",
		"review-comments":    "review.changes_requested",
		"merge-conflicts":    "merge.conflicts",
	}
	for key, want := range cases {
		if got := SemanticTypeForReaction(key); got != want {
			t.Fatalf("SemanticTypeForReaction(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestRendererPayloadIncludesSessionProjectIssueAndBranch(t *testing.T) {
	r := NewRenderer(fakeReader{rec: renderRecord()})
	row, err := r.Render(context.Background(), ports.Event{
		Type: "reaction.agent-needs-input", Priority: ports.PriorityUrgent,
		ProjectID: "ao", SessionID: "ao-7", Message: "needs input",
		Reaction:   &ports.ReactionEvent{Key: "agent-needs-input", Action: "notify"},
		OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != 3 || p.SemanticType != "session.needs_input" {
		t.Fatalf("payload header = %+v", p)
	}
	if p.Subject.Session == nil || p.Subject.Session.ID != "ao-7" || p.Subject.Session.ProjectID != "ao" {
		t.Fatalf("session subject missing: %+v", p.Subject.Session)
	}
	if p.Subject.Issue == nil || p.Subject.Issue.ID != "AO-12" || p.Subject.Branch != "feat/example" {
		t.Fatalf("issue/branch missing: %+v", p.Subject)
	}
}

func TestRendererPRPayloadIncludesFacts(t *testing.T) {
	r := NewRenderer(fakeReader{rec: renderRecord(), pr: domain.PRFacts{
		Exists: true, URL: "https://github.com/org/repo/pull/12", Number: 12,
		CI: domain.CIFailing, Review: domain.ReviewChangesRequest, Mergeability: domain.MergeConflicting,
	}})
	row, err := r.Render(context.Background(), ports.Event{
		Type: "reaction.review-comments", Priority: ports.PriorityAction,
		ProjectID: "ao", SessionID: "ao-7", Message: "review",
		Reaction: &ports.ReactionEvent{Key: "review-comments", Action: "notify"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Subject.PR == nil || p.Subject.PR.URL != "https://github.com/org/repo/pull/12" || p.Subject.PR.Number != 12 {
		t.Fatalf("pr subject missing: %+v", p.Subject.PR)
	}
	if p.CI == nil || p.CI.Status != "failing" {
		t.Fatalf("ci missing: %+v", p.CI)
	}
	if p.Review == nil || p.Review.Decision != "changes_requested" {
		t.Fatalf("review missing: %+v", p.Review)
	}
	if p.Merge == nil || p.Merge.Conflicts == nil || *p.Merge.Conflicts != true || p.Merge.Ready == nil || *p.Merge.Ready != false {
		t.Fatalf("merge missing: %+v", p.Merge)
	}
}

func TestRendererEscalationPayloadIncludesDetails(t *testing.T) {
	r := NewRenderer(fakeReader{rec: renderRecord()})
	row, err := r.Render(context.Background(), ports.Event{
		Type: "reaction.escalated", Priority: ports.PriorityUrgent,
		ProjectID: "ao", SessionID: "ao-7", Message: "escalated",
		Reaction:   &ports.ReactionEvent{Key: "ci-failed", Action: "escalated"},
		Escalation: &ports.EscalationEvent{Attempts: 3, Cause: "max_retries", DurationMs: 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Reaction == nil || p.Reaction.Key != "ci-failed" || p.Reaction.Action != "escalated" {
		t.Fatalf("reaction missing: %+v", p.Reaction)
	}
	if p.Escalation == nil || p.Escalation.Attempts != 3 || p.Escalation.Cause != "max_retries" || p.Escalation.DurationMs != 42 {
		t.Fatalf("escalation missing: %+v", p.Escalation)
	}
}

func renderRecord() domain.SessionRecord {
	return domain.SessionRecord{
		ID:        "ao-7",
		ProjectID: "ao",
		IssueID:   "AO-12",
		Lifecycle: domain.CanonicalSessionLifecycle{Session: domain.SessionSubstate{State: domain.SessionNeedsInput}},
		Metadata:  domain.SessionMetadata{Branch: "feat/example"},
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}
