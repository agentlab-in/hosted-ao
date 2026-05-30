package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

// GetMetadata returns the typed metadata for a session, or the zero value if the
// session has no metadata row yet.
func (s *Store) GetMetadata(ctx context.Context, id domain.SessionID) (domain.SessionMetadata, error) {
	row, err := s.q.GetSessionMetadata(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionMetadata{}, nil
	}
	if err != nil {
		return domain.SessionMetadata{}, fmt.Errorf("get metadata %s: %w", id, err)
	}
	return domain.SessionMetadata{
		Branch:          row.Branch,
		WorkspacePath:   row.WorkspacePath,
		RuntimeHandleID: row.RuntimeHandleID,
		RuntimeName:     row.RuntimeName,
		AgentSessionID:  row.AgentSessionID,
		Prompt:          row.Prompt,
	}, nil
}

// PatchMetadata merges meta into the session's metadata. It is outside the
// canonical write path: no revision bump, no CDC event. Empty fields are left
// unchanged (see UpsertSessionMetadata), so a partial patch is non-destructive.
func (s *Store) PatchMetadata(ctx context.Context, id domain.SessionID, meta domain.SessionMetadata) error {
	if meta.IsZero() {
		return nil
	}
	return s.q.UpsertSessionMetadata(ctx, gen.UpsertSessionMetadataParams{
		SessionID:       string(id),
		Branch:          meta.Branch,
		WorkspacePath:   meta.WorkspacePath,
		RuntimeHandleID: meta.RuntimeHandleID,
		RuntimeName:     meta.RuntimeName,
		AgentSessionID:  meta.AgentSessionID,
		Prompt:          meta.Prompt,
		UpdatedAt:       time.Now().UTC(),
	})
}
