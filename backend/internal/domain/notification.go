package domain

import (
	"encoding/json"
	"time"
)

// NotificationID is the stable public identifier for a persisted notification.
type NotificationID string

// Notification is the provider-neutral durable notification read model. It is
// sink-agnostic: desktop, dashboard, Slack, webhooks, etc. all consume the same
// semantic payload and actions later.
type Notification struct {
	Seq          int64
	ID           NotificationID
	ProjectID    ProjectID
	SessionID    SessionID
	Source       string
	EventType    string
	SemanticType string
	Priority     string
	Message      string
	Payload      json.RawMessage
	Actions      []NotificationAction
	DedupeKey    string
	CauseKey     string
	ReadAt       time.Time
	ArchivedAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NotificationAction is a provider-neutral action descriptor. Renderers may use
// Route for app-local navigation, URL for external navigation, or Method for a
// future command/action endpoint.
type NotificationAction struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Route  string `json:"route,omitempty"`
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}
