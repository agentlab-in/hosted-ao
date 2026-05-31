package notification

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var ErrDeliveryUpdateConflict = errors.New("notification delivery update conflict")

const (
	RouteDashboard = "dashboard"
	RouteDesktop   = "desktop"

	SinkAOApp   = "ao-app"
	SinkUnknown = "unknown"
)

type DeliveryStatus string

const (
	DeliveryQueued    DeliveryStatus = "queued"
	DeliveryLeased    DeliveryStatus = "leased"
	DeliverySent      DeliveryStatus = "sent"
	DeliveryRetryWait DeliveryStatus = "retry_wait"
	DeliveryFailed    DeliveryStatus = "failed"
	DeliverySkipped   DeliveryStatus = "skipped"
	DeliveryCancelled DeliveryStatus = "cancelled"
)

// DeliveryRow is the durable handoff state for one notification route. The
// backend creates AO-app rows; Electron claims them later and reports success or
// failure. External sinks can use the same shape in future issues.
type DeliveryRow struct {
	ID              string
	NotificationID  domain.NotificationID
	NotificationSeq int64
	ProjectID       domain.ProjectID
	SessionID       domain.SessionID

	RouteName      string
	Sink           string
	DestinationKey string
	RequestJSON    json.RawMessage

	Status        DeliveryStatus
	Attempts      int
	MaxAttempts   int
	NextAttemptAt time.Time
	LeaseOwner    string
	// LeaseExpiresAt is zero when the row is not leased.
	LeaseExpiresAt time.Time

	LastErrorCode string
	LastError     string
	ExternalID    string

	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeliveredAt time.Time
}

func NewDeliveryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate delivery id: %w", err)
	}
	return "del_" + hex.EncodeToString(b[:]), nil
}

func NormalizeDelivery(row DeliveryRow, now time.Time, maxAttempts int) (DeliveryRow, error) {
	if row.ID == "" {
		id, err := NewDeliveryID()
		if err != nil {
			return DeliveryRow{}, err
		}
		row.ID = id
	}
	if len(row.RequestJSON) == 0 {
		row.RequestJSON = json.RawMessage(`{}`)
	}
	if !json.Valid(row.RequestJSON) {
		return DeliveryRow{}, fmt.Errorf("invalid delivery request JSON for %s", row.ID)
	}
	if row.Status == "" {
		row.Status = DeliveryQueued
	}
	if row.MaxAttempts <= 0 {
		row.MaxAttempts = maxAttempts
	}
	if row.MaxAttempts <= 0 {
		row.MaxAttempts = 1
	}
	if row.NextAttemptAt.IsZero() {
		row.NextAttemptAt = now
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = row.CreatedAt
	}
	return row, nil
}

func TerminalStatus(s DeliveryStatus) bool {
	switch s {
	case DeliverySent, DeliveryFailed, DeliverySkipped, DeliveryCancelled:
		return true
	default:
		return false
	}
}
