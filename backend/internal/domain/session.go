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

// SessionRecord is the PERSISTENCE shape: identity, canonical lifecycle, and
// metadata — everything the store holds, and nothing derived. The store reads
// and writes records; it never produces the derived display status.
type SessionRecord struct {
	ID        SessionID                 `json:"id"`
	ProjectID ProjectID                 `json:"projectId"`
	IssueID   IssueID                   `json:"issueId,omitempty"`
	Kind      SessionKind               `json:"kind"`
	Lifecycle CanonicalSessionLifecycle `json:"lifecycle"`
	Metadata  map[string]string         `json:"metadata,omitempty"`
	CreatedAt time.Time                 `json:"createdAt"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}

// Session is the read-model returned across the API boundary (to controllers,
// then the frontend): a SessionRecord plus the DERIVED display Status. The
// Session Manager is the single producer of Status — it builds a Session from a
// stored SessionRecord by calling DeriveLegacyStatus, so the store and API
// never recompute (or accidentally persist) it.
type Session struct {
	SessionRecord
	Status SessionStatus `json:"status"`
}
