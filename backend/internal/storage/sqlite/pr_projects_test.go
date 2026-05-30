package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProjectUpsertGetListDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if _, ok, err := s.GetProject(ctx, "p1"); err != nil || ok {
		t.Fatalf("get missing: ok=%v err=%v", ok, err)
	}

	p := ProjectRow{
		ID: "p1", Path: "/repo", RepoOwner: "acme", RepoName: "widget",
		RepoPlatform: "github", RepoOriginURL: "git@github.com:acme/widget.git",
		DefaultBranch: "main", DisplayName: "Widget", SessionPrefix: "wid",
		Source: "local", RegisteredAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := s.GetProject(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != p {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	// Upsert again with a changed field updates in place (no duplicate).
	p.DisplayName = "Widget 2"
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].DisplayName != "Widget 2" {
		t.Fatalf("list after re-upsert = %+v", list)
	}

	if err := s.DeleteProject(ctx, "p1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := s.GetProject(ctx, "p1"); ok {
		t.Fatal("project should be gone after delete")
	}
}

func TestArchiveProjectHidesFromListButGetResolves(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.UpsertProject(ctx, ProjectRow{ID: "p1", Path: "/repo", RegisteredAt: now}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.ArchiveProject(ctx, "p1", now); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Active-only list hides it.
	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("archived project should not appear in ListProjects, got %+v", list)
	}

	// Get still resolves it (a session's project_id must not dangle) and reports
	// the archived marker.
	got, ok, err := s.GetProject(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("get archived: ok=%v err=%v", ok, err)
	}
	if got.ArchivedAt.IsZero() {
		t.Fatal("archived project should carry a non-zero ArchivedAt")
	}
}

func TestPREnrichmentUpsertGetDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// pr_enrichment FKs sessions(id); seed the session first.
	if err := s.Upsert(ctx, sampleRecord("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if _, ok, err := s.GetPREnrichment(ctx, "s1"); err != nil || ok {
		t.Fatalf("get missing: ok=%v err=%v", ok, err)
	}

	e := PREnrichmentRow{
		SessionID: "s1", CISummary: "3 passing, 1 failing", ReviewDecision: "changes_requested",
		Mergeability: "blocked", PendingComments: `[{"path":"a.go"}]`, CILogTail: "FAIL TestX",
		LastFetchedAt: now,
	}
	if err := s.UpsertPREnrichment(ctx, e); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := s.GetPREnrichment(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != e {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, e)
	}

	if err := s.DeletePREnrichment(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := s.GetPREnrichment(ctx, "s1"); ok {
		t.Fatal("enrichment should be gone after delete")
	}
}
