// Package handoff coordinates durable execution ownership across two daemons.
// Network clients relay authenticated requests, but cannot infer ownership from
// a timeout or restart a controller merely by replaying an activation request.
package handoff

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Runtime owns local session operations. Freeze must drain input, stop all
// workspace writers and prove the source stopped before snapshotting. Prepare
// must stage and validate without launching. Activate must use h.ID as a stable
// controller generation. ConfirmActive may return true only for that exact
// generation, never for an unrelated process or an unknown probe result.
type Runtime interface {
	Freeze(context.Context, domain.MachineHandoff) (checkpointHash string, err error)
	Prepare(context.Context, domain.MachineHandoff) error
	Activate(context.Context, domain.MachineHandoff) error
	ConfirmActive(context.Context, domain.MachineHandoff) (bool, error)
}

// Service coordinates one daemon's side of a handoff. Runtime adapters must
// join its durable reservations to the daemon's existing session mutation gate
// before this coordinator can be exposed to clients.
type Service struct {
	// One coordinator per daemon serializes local effects with cancellation.
	// Durable CAS still arbitrates competing activation requests after restart.
	gate    chan struct{}
	store   ports.MachineHandoffStore
	runtime Runtime
	machine string
}

// New constructs the single coordinator for a daemon with a stable machine ID.
func New(store ports.MachineHandoffStore, runtime Runtime, machine string) *Service {
	return &Service{gate: make(chan struct{}, 1), store: store, runtime: runtime, machine: machine}
}

// Start reserves a source before touching its runtime. Repeating an ID with a
// different destination/session is a conflict, not a new transfer.
func (s *Service) Start(ctx context.Context, id string, session domain.SessionID, destination string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return domain.MachineHandoff{}, err
	}
	token := hex.EncodeToString(secret)
	now := time.Now().UTC()
	h := domain.MachineHandoff{ID: id, SessionID: session, SourceMachine: s.machine,
		DestinationMachine: destination, Role: domain.HandoffSource, State: domain.HandoffFreezing,
		ActivationSecret: token, ActivationHash: domain.HandoffTokenHash(token), CreatedAt: now, UpdatedAt: now}
	if err := h.Validate(); err != nil {
		return domain.MachineHandoff{}, err
	}
	h, _, err = s.store.CreateMachineHandoff(ctx, h)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	return public(h), nil
}

// Freeze is retryable only while freezing. Runtime implementations serialize
// local effects by session and must return the same immutable checkpoint once
// written. An error keeps the durable fence closed for explicit recovery.
func (s *Service) Freeze(ctx context.Context, id string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	h, err := s.get(ctx, id, domain.HandoffSource)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if h.State == domain.HandoffCheckpointReady || h.State == domain.HandoffRelinquished {
		return public(h), nil
	}
	if h.State != domain.HandoffFreezing {
		return public(h), domain.ErrMachineHandoffConflict
	}
	hash, err := s.runtime.Freeze(ctx, h)
	if err != nil {
		return public(h), err
	}
	h.CheckpointHash = hash
	return s.advance(ctx, h, domain.HandoffCheckpointReady)
}

// Import reserves the destination before any workspace or session is created.
// h is the authenticated checkpoint's manifest, not an arbitrary session row.
func (s *Service) Import(ctx context.Context, h domain.MachineHandoff) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	if h.DestinationMachine != s.machine || h.Role != domain.HandoffSource || h.State != domain.HandoffCheckpointReady || h.ActivationSecret != "" {
		return domain.MachineHandoff{}, domain.ErrMachineHandoffInvalid
	}
	now := time.Now().UTC()
	h.Role, h.State, h.Revision = domain.HandoffDestination, domain.HandoffPreparing, 0
	h.CreatedAt, h.UpdatedAt = now, now
	if err := h.Validate(); err != nil {
		return domain.MachineHandoff{}, err
	}
	h, _, err = s.store.CreateMachineHandoff(ctx, h)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	return public(h), nil
}

// Prepare validates the staged destination before source relinquishment.
func (s *Service) Prepare(ctx context.Context, id string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	h, err := s.get(ctx, id, domain.HandoffDestination)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if h.State != domain.HandoffPreparing {
		return public(h), nil
	}
	if err := s.runtime.Prepare(ctx, h); err != nil {
		return public(h), err
	}
	return s.advance(ctx, h, domain.HandoffPrepared)
}

// Relinquish returns the activation secret only AFTER the source's irreversible
// ownership transition commits. A lost response can safely be retried. Receipt
// must come from the authenticated destination's prepare response.
func (s *Service) Relinquish(ctx context.Context, id string, receipt domain.MachineHandoff) (string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	h, err := s.get(ctx, id, domain.HandoffSource)
	if err != nil {
		return "", err
	}
	if receipt.Role != domain.HandoffDestination || receipt.State != domain.HandoffPrepared ||
		receipt.ID != h.ID || receipt.SessionID != h.SessionID || receipt.SourceMachine != h.SourceMachine ||
		receipt.DestinationMachine != h.DestinationMachine || receipt.CheckpointHash != h.CheckpointHash || receipt.ActivationHash != h.ActivationHash {
		return "", domain.ErrMachineHandoffConflict
	}
	if h.State != domain.HandoffRelinquished {
		if _, err := s.advance(ctx, h, domain.HandoffRelinquished); err != nil {
			return "", err
		}
	}
	return h.ActivationSecret, nil
}

// Activate deliberately does not retry a side effect after an ambiguous
// outcome. Only the caller winning prepared -> activating may invoke Activate.
// A restarted daemon reconciles the recorded generation with ConfirmActive.
func (s *Service) Activate(ctx context.Context, id, token string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	h, err := s.get(ctx, id, domain.HandoffDestination)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	actual := domain.HandoffTokenHash(token)
	if len(token) != 64 || subtle.ConstantTimeCompare([]byte(actual), []byte(h.ActivationHash)) != 1 {
		return public(h), domain.ErrMachineHandoffInvalid
	}
	if h.State == domain.HandoffActive {
		return public(h), nil
	}
	if h.State == domain.HandoffActivating {
		return s.reconcile(ctx, id)
	}
	h, err = s.advance(ctx, h, domain.HandoffActivating)
	if err != nil {
		return h, err
	}
	if err := s.runtime.Activate(ctx, h); err != nil {
		return h, fmt.Errorf("%w: %w", domain.ErrMachineHandoffUncertain, err)
	}
	return s.reconcile(ctx, id)
}

// Reconcile confirms an exact destination generation without relaunching it.
func (s *Service) Reconcile(ctx context.Context, id string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	return s.reconcile(ctx, id)
}

func (s *Service) reconcile(ctx context.Context, id string) (domain.MachineHandoff, error) {
	h, err := s.get(ctx, id, domain.HandoffDestination)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if h.State == domain.HandoffActive {
		return public(h), nil
	}
	if h.State != domain.HandoffActivating {
		return public(h), domain.ErrMachineHandoffConflict
	}
	active, err := s.runtime.ConfirmActive(ctx, h)
	if err != nil || !active {
		return public(h), domain.ErrMachineHandoffUncertain
	}
	return s.advance(ctx, h, domain.HandoffActive)
}

// Cancel releases only an untransferred source. It never launches a process:
// normal explicit session recovery decides whether a stopped source can resume.
// Runtime Freeze and cancellation must share the local session mutation lock.
func (s *Service) Cancel(ctx context.Context, id string) (domain.MachineHandoff, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	defer release()
	h, err := s.get(ctx, id, domain.HandoffSource)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if h.State == domain.HandoffCancelled {
		return public(h), nil
	}
	return s.advance(ctx, h, domain.HandoffCancelled)
}

// Get returns progress without the private activation grant.
func (s *Service) Get(ctx context.Context, id string, role domain.HandoffRole) (domain.MachineHandoff, error) {
	h, err := s.get(ctx, id, role)
	return public(h), err
}

func (s *Service) get(ctx context.Context, id string, role domain.HandoffRole) (domain.MachineHandoff, error) {
	h, ok, err := s.store.GetMachineHandoff(ctx, id, role)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if !ok {
		return domain.MachineHandoff{}, domain.ErrMachineHandoffNotFound
	}
	if (role == domain.HandoffSource && h.SourceMachine != s.machine) || (role == domain.HandoffDestination && h.DestinationMachine != s.machine) {
		return domain.MachineHandoff{}, domain.ErrMachineHandoffConflict
	}
	return h, nil
}

func (s *Service) advance(ctx context.Context, h domain.MachineHandoff, state domain.HandoffState) (domain.MachineHandoff, error) {
	if !h.CanAdvance(state) {
		return public(h), domain.ErrMachineHandoffConflict
	}
	previous, revision := h.State, h.Revision
	h.State, h.Revision, h.UpdatedAt = state, revision+1, time.Now().UTC()
	if err := h.Validate(); err != nil {
		return domain.MachineHandoff{}, err
	}
	ok, err := s.store.AdvanceMachineHandoff(ctx, h, previous, revision)
	if err != nil {
		return domain.MachineHandoff{}, err
	}
	if !ok {
		return domain.MachineHandoff{}, domain.ErrMachineHandoffConflict
	}
	return public(h), nil
}

func public(h domain.MachineHandoff) domain.MachineHandoff {
	h.ActivationSecret = ""
	return h
}

func (s *Service) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			return nil, err
		}
		return func() { <-s.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
