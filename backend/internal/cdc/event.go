// Package cdc is the change-data-capture pipeline that turns the storage layer's
// transactional outbox into a durable, ordered event stream for the frontend.
//
// The flow: the publisher drains the SQLite outbox (sent=0, seq order) and
// appends each change as one JSON line to a rotating log file. The consumer
// tails that file from a durable byte cursor, deduplicates by seq, and fans each
// change out through the Broadcaster to in-process subscribers (the WS/SSE
// transport, wired later). The janitor reclaims outbox rows every consumer has
// acknowledged. Delivery is at-least-once; seq is the idempotency key.
package cdc

import "time"

// Event is one change-data-capture record. It is the JSONL line shape and the
// value handed to Broadcaster subscribers. Seq is the monotonic ordering and
// idempotency key (the change_log seq).
type Event struct {
	Seq       int64     `json:"seq"`
	SessionID string    `json:"sessionId"`
	EventType string    `json:"eventType"`
	Revision  int64     `json:"revision"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

// resetMarker is written as the first line of a freshly rotated log file. A
// consumer that reads it knows the byte offsets of the previous file are void
// and must snapshot-resync, then resume from the current MAX(seq).
type resetMarker struct {
	Type      string    `json:"type"` // always "reset"
	RotatedAt time.Time `json:"rotatedAt"`
}
