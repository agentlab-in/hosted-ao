package notification

// PayloadSchemaVersion is the durable notification payload contract version.
const PayloadSchemaVersion = 3

// Payload is the provider-neutral, rich notification data shape persisted in
// SQLite. It intentionally mirrors legacy AO's NotificationData V3 while only
// filling fields the Go rewrite can source today.
type Payload struct {
	SchemaVersion int                `json:"schemaVersion"`
	SemanticType  string             `json:"semanticType"`
	Subject       SubjectPayload     `json:"subject"`
	Reaction      *ReactionPayload   `json:"reaction,omitempty"`
	Escalation    *EscalationPayload `json:"escalation,omitempty"`
	CI            *CIPayload         `json:"ci,omitempty"`
	Review        *ReviewPayload     `json:"review,omitempty"`
	Merge         *MergePayload      `json:"merge,omitempty"`
}

// SubjectPayload identifies what a notification is about — the session and,
// when relevant, its PR, issue, and branch.
type SubjectPayload struct {
	Session *SessionSubjectPayload `json:"session,omitempty"`
	PR      *PRSubjectPayload      `json:"pr,omitempty"`
	Issue   *IssueSubjectPayload   `json:"issue,omitempty"`
	Branch  string                 `json:"branch,omitempty"`
}

// SessionSubjectPayload identifies the session a notification concerns.
type SessionSubjectPayload struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
}

// PRSubjectPayload identifies the PR a notification concerns.
type PRSubjectPayload struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
}

// IssueSubjectPayload identifies the tracker issue a notification concerns.
type IssueSubjectPayload struct {
	ID string `json:"id,omitempty"`
}

// ReactionPayload carries the reaction that produced the notification.
type ReactionPayload struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// EscalationPayload carries the escalation that produced the notification.
type EscalationPayload struct {
	Attempts   int    `json:"attempts"`
	Cause      string `json:"cause"`
	DurationMs int64  `json:"durationMs"`
}

// CIPayload is the CI context of a notification.
type CIPayload struct {
	Status string `json:"status"`
}

// ReviewPayload is the review context of a notification.
type ReviewPayload struct {
	Decision string `json:"decision"`
}

// MergePayload is the merge-readiness context of a notification.
type MergePayload struct {
	Ready     *bool `json:"ready,omitempty"`
	Conflicts *bool `json:"conflicts,omitempty"`
	IsBehind  *bool `json:"isBehind,omitempty"`
}
