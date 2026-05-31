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

type SubjectPayload struct {
	Session *SessionSubjectPayload `json:"session,omitempty"`
	PR      *PRSubjectPayload      `json:"pr,omitempty"`
	Issue   *IssueSubjectPayload   `json:"issue,omitempty"`
	Branch  string                 `json:"branch,omitempty"`
}

type SessionSubjectPayload struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
}

type PRSubjectPayload struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
}

type IssueSubjectPayload struct {
	ID string `json:"id,omitempty"`
}

type ReactionPayload struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

type EscalationPayload struct {
	Attempts   int    `json:"attempts"`
	Cause      string `json:"cause"`
	DurationMs int64  `json:"durationMs"`
}

type CIPayload struct {
	Status string `json:"status"`
}

type ReviewPayload struct {
	Decision string `json:"decision"`
}

type MergePayload struct {
	Ready     *bool `json:"ready,omitempty"`
	Conflicts *bool `json:"conflicts,omitempty"`
	IsBehind  *bool `json:"isBehind,omitempty"`
}
