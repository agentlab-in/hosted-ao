package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Store is the SQLite-backed ports.LifecycleStore. The LCM is its sole logical
// writer (via Upsert); readers (Session Manager, reaper) use Load/Get/List.
type Store struct {
	db *sql.DB
	q  *gen.Queries
}

var _ ports.LifecycleStore = (*Store)(nil)

// NewStore wraps an opened *sql.DB (see Open) as a LifecycleStore.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, q: gen.New(db)}
}

// Load returns the canonical lifecycle for a session, or ok=false if absent.
func (s *Store) Load(ctx context.Context, id domain.SessionID) (domain.CanonicalSessionLifecycle, bool, error) {
	row, err := s.q.GetSession(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CanonicalSessionLifecycle{}, false, nil
	}
	if err != nil {
		return domain.CanonicalSessionLifecycle{}, false, fmt.Errorf("load session %s: %w", id, err)
	}
	return rowToLifecycle(row), true, nil
}

// Get returns the full record (no derived status) for a session.
func (s *Store) Get(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	row, err := s.q.GetSession(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionRecord{}, false, nil
	}
	if err != nil {
		return domain.SessionRecord{}, false, fmt.Errorf("get session %s: %w", id, err)
	}
	return rowToRecord(row), true, nil
}

// List returns every record for a project (no archive filter — mirrors the
// in-memory store contract; terminal filtering is the caller's job).
func (s *Store) List(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	rows, err := s.q.ListSessionsByProject(ctx, string(project))
	if err != nil {
		return nil, fmt.Errorf("list sessions for %s: %w", project, err)
	}
	out := make([]domain.SessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToRecord(row))
	}
	return out, nil
}

// ListAll returns every persisted session across all projects. The CDC snapshot
// source uses it to rebuild current state after a log-rotation gap.
func (s *Store) ListAll(ctx context.Context) ([]domain.SessionRecord, error) {
	rows, err := s.q.ListAllSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all sessions: %w", err)
	}
	out := make([]domain.SessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToRecord(row))
	}
	return out, nil
}

// GetMetadata returns the opaque key/value metadata for a session.
func (s *Store) GetMetadata(ctx context.Context, id domain.SessionID) (map[string]string, error) {
	rows, err := s.q.GetMetadata(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("get metadata %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

// PatchMetadata merges kv into the session's metadata. It is outside the
// canonical write path: no revision bump, no CDC event.
func (s *Store) PatchMetadata(ctx context.Context, id domain.SessionID, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin patch metadata: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	for k, v := range kv {
		if err := qtx.UpsertMetadata(ctx, gen.UpsertMetadataParams{
			SessionID: string(id),
			Key:       k,
			Value:     v,
		}); err != nil {
			return fmt.Errorf("patch metadata %s[%s]: %w", id, k, err)
		}
	}
	return tx.Commit()
}
