package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// MachineHandoff records execution ownership, not display status. A source
// tombstone must survive completion: a lost acknowledgement cannot authorize
// a second execution on the source.
type MachineHandoff struct {
	ID                 string       `json:"id"`
	SessionID          SessionID    `json:"sessionId"`
	SourceMachine      string       `json:"sourceMachine"`
	DestinationMachine string       `json:"destinationMachine"`
	Role               HandoffRole  `json:"role"`
	State              HandoffState `json:"state"`
	CheckpointHash     string       `json:"checkpointHash"`
	ActivationHash     string       `json:"activationHash"`
	// ActivationSecret never appears in a read model or portable checkpoint.
	ActivationSecret string    `json:"-"`
	Revision         int64     `json:"revision"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// HandoffRole identifies which side of the transfer owns this local record.
type HandoffRole string

// HandoffState is a durable checkpoint in the ownership protocol.
type HandoffState string

// Roles and states accepted by the ownership protocol.
const (
	HandoffSource          HandoffRole  = "source"
	HandoffDestination     HandoffRole  = "destination"
	HandoffFreezing        HandoffState = "freezing"
	HandoffCheckpointReady HandoffState = "checkpoint_ready"
	HandoffRelinquished    HandoffState = "relinquished"
	HandoffCancelled       HandoffState = "cancelled"
	HandoffPreparing       HandoffState = "preparing"
	HandoffPrepared        HandoffState = "prepared"
	HandoffActivating      HandoffState = "activating"
	HandoffActive          HandoffState = "active"
)

// Machine handoff errors distinguish invalid input, conflicts, and uncertainty.
var (
	ErrMachineHandoffConflict  = errors.New("machine handoff conflicts with durable ownership")
	ErrMachineHandoffInvalid   = errors.New("invalid machine handoff")
	ErrMachineHandoffNotFound  = errors.New("machine handoff not found")
	ErrMachineHandoffUncertain = errors.New("machine handoff activation outcome is unknown; reconcile the destination before retrying")
)

// FencesSession is deliberately true for a relinquished source forever, and
// for an activating destination until its exact controller has been confirmed.
func (h MachineHandoff) FencesSession() bool {
	return h.State != HandoffCancelled && h.State != HandoffActive
}

// CanAdvance rejects transitions that could reopen relinquished ownership.
func (h MachineHandoff) CanAdvance(next HandoffState) bool {
	if h.Role == HandoffSource {
		switch h.State {
		case HandoffFreezing:
			return next == HandoffCheckpointReady || next == HandoffCancelled
		case HandoffCheckpointReady:
			return next == HandoffRelinquished || next == HandoffCancelled
		}
	}
	if h.Role == HandoffDestination {
		switch h.State {
		case HandoffPreparing:
			return next == HandoffPrepared
		case HandoffPrepared:
			return next == HandoffActivating
		case HandoffActivating:
			return next == HandoffActive
		}
	}
	return false
}

// Validate checks identity, state, and the local activation secret boundary.
func (h MachineHandoff) Validate() error {
	if strings.TrimSpace(h.ID) == "" || strings.TrimSpace(string(h.SessionID)) == "" ||
		strings.TrimSpace(h.SourceMachine) == "" || strings.TrimSpace(h.DestinationMachine) == "" ||
		h.SourceMachine == h.DestinationMachine || len(h.ID) > 128 || len(h.SessionID) > 128 ||
		len(h.SourceMachine) > 128 || len(h.DestinationMachine) > 128 || h.Revision < 0 ||
		h.CreatedAt.IsZero() || h.UpdatedAt.Before(h.CreatedAt) || !validHandoffHash(h.ActivationHash) {
		return ErrMachineHandoffInvalid
	}
	switch h.Role {
	case HandoffSource:
		if h.State != HandoffFreezing && h.State != HandoffCheckpointReady && h.State != HandoffRelinquished && h.State != HandoffCancelled {
			return ErrMachineHandoffInvalid
		}
		if len(h.ActivationSecret) != 64 || HandoffTokenHash(h.ActivationSecret) != h.ActivationHash {
			return ErrMachineHandoffInvalid
		}
	case HandoffDestination:
		if h.ActivationSecret != "" || (h.State != HandoffPreparing && h.State != HandoffPrepared && h.State != HandoffActivating && h.State != HandoffActive) {
			return ErrMachineHandoffInvalid
		}
	default:
		return ErrMachineHandoffInvalid
	}
	missingCheckpointAllowed := (h.State == HandoffFreezing || h.State == HandoffCancelled) && h.CheckpointHash == ""
	if !validHandoffHash(h.CheckpointHash) && !missingCheckpointAllowed {
		return ErrMachineHandoffInvalid
	}
	return nil
}

// HandoffTokenHash derives the verifier carried by a portable manifest.
func HandoffTokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func validHandoffHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
