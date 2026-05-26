package domain

import "time"

// SessionID, ProjectID, IssueID are distinct string types so they can't be
// swapped at a call site by accident.
type (
	SessionID string
	ProjectID string
	IssueID   string
)

type SessionKind string

const (
	KindWorker       SessionKind = "worker"
	KindOrchestrator SessionKind = "orchestrator"
)

// Session is the read-model returned across the API boundary (to controllers,
// then the frontend). Status is the DERIVED display status, attached on read by
// the Session Manager so the API layer never recomputes it (single producer).
type Session struct {
	ID        SessionID                 `json:"id"`
	ProjectID ProjectID                 `json:"projectId"`
	IssueID   IssueID                   `json:"issueId,omitempty"`
	Kind      SessionKind               `json:"kind"`
	Lifecycle CanonicalSessionLifecycle `json:"lifecycle"`
	Status    SessionStatus             `json:"status"`
	Metadata  map[string]string         `json:"metadata,omitempty"`
	CreatedAt time.Time                 `json:"createdAt"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}
