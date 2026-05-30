package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ProjectRow is one registered repo, the durable twin of the old YAML config
// entry. It is the unit the registration path upserts and cross-project readers
// list. Off the canonical CDC path: writing a project never emits a change_log
// or outbox event.
type ProjectRow struct {
	ID            string
	Path          string
	RepoOwner     string
	RepoName      string
	RepoPlatform  string
	RepoOriginURL string
	DefaultBranch string
	DisplayName   string
	SessionPrefix string
	Source        string
	RegisteredAt  time.Time
	// ArchivedAt is the soft-delete marker; zero means active. GetProject returns
	// it regardless of state (so a session can resolve its archived project);
	// ListProjects returns only rows where it is zero.
	ArchivedAt time.Time
}

// UpsertProject inserts or updates one registered project.
func (s *Store) UpsertProject(ctx context.Context, r ProjectRow) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.q.UpsertProject(ctx, gen.UpsertProjectParams{
		ID:            r.ID,
		Path:          r.Path,
		RepoOwner:     r.RepoOwner,
		RepoName:      r.RepoName,
		RepoPlatform:  r.RepoPlatform,
		RepoOriginUrl: r.RepoOriginURL,
		DefaultBranch: r.DefaultBranch,
		DisplayName:   r.DisplayName,
		SessionPrefix: r.SessionPrefix,
		Source:        r.Source,
		RegisteredAt:  r.RegisteredAt,
		ArchivedAt:    nullTime(r.ArchivedAt),
	})
}

// ArchiveProject soft-deletes one project, keeping the row so a session's
// project_id still resolves. Active-only reads (ListProjects) then hide it.
func (s *Store) ArchiveProject(ctx context.Context, id string, t time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.q.ArchiveProject(ctx, gen.ArchiveProjectParams{
		ArchivedAt: nullTime(t),
		ID:         id,
	})
}

// GetProject returns one project by id. ok is false when no row exists.
func (s *Store) GetProject(ctx context.Context, id string) (ProjectRow, bool, error) {
	p, err := s.q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectRow{}, false, nil
	}
	if err != nil {
		return ProjectRow{}, false, fmt.Errorf("get project: %w", err)
	}
	return projectRowFromGen(p), true, nil
}

// ListProjects returns every registered project, ordered by id.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectRow, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]ProjectRow, 0, len(rows))
	for _, p := range rows {
		out = append(out, projectRowFromGen(p))
	}
	return out, nil
}

// DeleteProject removes one project by id.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.q.DeleteProject(ctx, id)
}

func projectRowFromGen(p gen.Project) ProjectRow {
	return ProjectRow{
		ID:            p.ID,
		Path:          p.Path,
		RepoOwner:     p.RepoOwner,
		RepoName:      p.RepoName,
		RepoPlatform:  p.RepoPlatform,
		RepoOriginURL: p.RepoOriginUrl,
		DefaultBranch: p.DefaultBranch,
		DisplayName:   p.DisplayName,
		SessionPrefix: p.SessionPrefix,
		Source:        p.Source,
		RegisteredAt:  p.RegisteredAt,
		ArchivedAt:    p.ArchivedAt.Time,
	}
}

// nullTime maps a zero time.Time to a NULL column, else a valid timestamp.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
