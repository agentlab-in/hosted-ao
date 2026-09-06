package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

var _ ports.MachineHandoffStore = (*Store)(nil)

// CreateMachineHandoff reserves a session or returns the matching retry record.
func (s *Store) CreateMachineHandoff(ctx context.Context, h domain.MachineHandoff) (domain.MachineHandoff, bool, error) {
	if err := h.Validate(); err != nil {
		return domain.MachineHandoff{}, false, err
	}
	if h.Revision != 0 || (h.Role == domain.HandoffSource && h.State != domain.HandoffFreezing) ||
		(h.Role == domain.HandoffDestination && h.State != domain.HandoffPreparing) {
		return domain.MachineHandoff{}, false, domain.ErrMachineHandoffInvalid
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.MachineHandoff{}, false, err
	}
	defer s.writeMu.Unlock()
	n, err := s.qw.InsertMachineHandoff(ctx, gen.InsertMachineHandoffParams{
		ID: h.ID, SessionID: string(h.SessionID), SourceMachine: h.SourceMachine,
		DestinationMachine: h.DestinationMachine, Role: string(h.Role), State: string(h.State),
		CheckpointHash: h.CheckpointHash, ActivationHash: h.ActivationHash, ActivationSecret: h.ActivationSecret,
		Revision: h.Revision, CreatedAt: h.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: h.UpdatedAt.Format(time.RFC3339Nano),
	})
	if err != nil || n == 1 {
		return h, n == 1, err
	}
	row, err := s.qw.GetMachineHandoff(ctx, gen.GetMachineHandoffParams{ID: h.ID, Role: string(h.Role)})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MachineHandoff{}, false, domain.ErrMachineHandoffConflict
	}
	if err != nil {
		return domain.MachineHandoff{}, false, err
	}
	existing, err := machineHandoffFromRow(row)
	if err != nil {
		return domain.MachineHandoff{}, false, err
	}
	if existing.SessionID != h.SessionID || existing.SourceMachine != h.SourceMachine || existing.DestinationMachine != h.DestinationMachine ||
		(h.Role == domain.HandoffDestination && (existing.CheckpointHash != h.CheckpointHash || existing.ActivationHash != h.ActivationHash)) {
		return domain.MachineHandoff{}, false, domain.ErrMachineHandoffConflict
	}
	return existing, false, nil
}

// GetMachineHandoff reads a local ownership record, including its private grant.
func (s *Store) GetMachineHandoff(ctx context.Context, id string, role domain.HandoffRole) (domain.MachineHandoff, bool, error) {
	row, err := s.qr.GetMachineHandoff(ctx, gen.GetMachineHandoffParams{ID: id, Role: string(role)})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MachineHandoff{}, false, nil
	}
	if err != nil {
		return domain.MachineHandoff{}, false, err
	}
	h, err := machineHandoffFromRow(row)
	return h, err == nil, err
}

// AdvanceMachineHandoff atomically changes a state while preserving identity.
func (s *Store) AdvanceMachineHandoff(ctx context.Context, h domain.MachineHandoff, expected domain.HandoffState, revision int64) (bool, error) {
	if err := h.Validate(); err != nil {
		return false, err
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.writeMu.Unlock()
	row, err := s.qw.GetMachineHandoff(ctx, gen.GetMachineHandoffParams{ID: h.ID, Role: string(h.Role)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	previous, err := machineHandoffFromRow(row)
	if err != nil {
		return false, err
	}
	if previous.State != expected || previous.Revision != revision {
		return false, nil
	}
	if !previous.CanAdvance(h.State) || h.Revision != revision+1 || previous.SessionID != h.SessionID ||
		previous.SourceMachine != h.SourceMachine || previous.DestinationMachine != h.DestinationMachine ||
		previous.ActivationHash != h.ActivationHash || previous.ActivationSecret != h.ActivationSecret ||
		!previous.CreatedAt.Equal(h.CreatedAt) || h.UpdatedAt.Before(previous.UpdatedAt) ||
		(previous.CheckpointHash != h.CheckpointHash && (expected != domain.HandoffFreezing || h.State != domain.HandoffCheckpointReady)) {
		return false, domain.ErrMachineHandoffConflict
	}
	n, err := s.qw.AdvanceMachineHandoff(ctx, gen.AdvanceMachineHandoffParams{
		ID: h.ID, Role: string(h.Role), NextState: string(h.State), CheckpointHash: h.CheckpointHash,
		UpdatedAt: h.UpdatedAt.Format(time.RFC3339Nano), ExpectedState: string(expected), ExpectedRevision: revision,
	})
	return n == 1, err
}

// MachineHandoffFencesSession reads the durable execution reservation.
func (s *Store) MachineHandoffFencesSession(ctx context.Context, id domain.SessionID) (bool, error) {
	n, err := s.qr.MachineHandoffFencesSession(ctx, string(id))
	return n, err
}

func machineHandoffFromRow(row gen.MachineHandoff) (domain.MachineHandoff, error) {
	created, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	updated, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	h := domain.MachineHandoff{ID: row.ID, SessionID: domain.SessionID(row.SessionID),
		SourceMachine: row.SourceMachine, DestinationMachine: row.DestinationMachine,
		Role: domain.HandoffRole(row.Role), State: domain.HandoffState(row.State),
		CheckpointHash: row.CheckpointHash, ActivationHash: row.ActivationHash, ActivationSecret: row.ActivationSecret,
		Revision: row.Revision, CreatedAt: created, UpdatedAt: updated}
	return h, h.Validate()
}
