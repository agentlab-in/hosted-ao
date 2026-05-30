package sqlite

import (
	"context"
	"reflect"
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

func TestPRUpsertGetDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// pr FKs sessions(id); seed the session first.
	if err := s.Upsert(ctx, sampleRecord("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if _, ok, err := s.GetPR(ctx, "s1"); err != nil || ok {
		t.Fatalf("get missing: ok=%v err=%v", ok, err)
	}

	pr := PRRow{
		SessionID: "s1", ReviewDecision: "changes_requested", Mergeability: "blocked",
		CIState: "failing", CIPassed: 3, CIFailed: 1, CIPending: 0, CILogTail: "FAIL TestX",
		LastFetchedAt: now,
	}
	if err := s.UpsertPR(ctx, pr); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := s.GetPR(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != pr {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, pr)
	}

	if err := s.DeletePR(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := s.GetPR(ctx, "s1"); ok {
		t.Fatal("pr should be gone after delete")
	}
}

func TestPRRejectsBadEnum(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, sampleRecord("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// review_decision is a CHECK-constrained enum; an off-list value must fail.
	err := s.UpsertPR(ctx, PRRow{
		SessionID: "s1", ReviewDecision: "definitely_not_a_decision",
		Mergeability: "unknown", CIState: "unknown", LastFetchedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected CHECK constraint to reject an invalid review_decision")
	}
}

func TestPRChecksAndCommentsReplaceAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.Upsert(ctx, sampleRecord("s1"), ports.EventSessionCreated); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// pr_check / pr_comment FK pr(session_id); the pr row must exist first.
	if err := s.UpsertPR(ctx, PRRow{
		SessionID: "s1", ReviewDecision: "review_required", Mergeability: "unknown",
		CIState: "pending", LastFetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}

	checks := []PRCheck{
		{Name: "build", Status: "passed", URL: "https://ci/build"},
		{Name: "test", Status: "failed", URL: "https://ci/test"},
	}
	if err := s.ReplacePRChecks(ctx, "s1", checks); err != nil {
		t.Fatalf("replace checks: %v", err)
	}
	gotChecks, err := s.ListPRChecks(ctx, "s1")
	if err != nil {
		t.Fatalf("list checks: %v", err)
	}
	if !reflect.DeepEqual(gotChecks, checks) {
		t.Fatalf("checks = %+v, want %+v", gotChecks, checks)
	}
	// Replace is a set-replace, not a merge: a shorter set removes the rest.
	if err := s.ReplacePRChecks(ctx, "s1", []PRCheck{{Name: "build", Status: "passed"}}); err != nil {
		t.Fatalf("replace checks 2: %v", err)
	}
	if gotChecks, _ = s.ListPRChecks(ctx, "s1"); len(gotChecks) != 1 {
		t.Fatalf("after replace, checks = %+v, want 1", gotChecks)
	}

	comments := []PRComment{
		{CommentID: "c1", Author: "alice", File: "a.go", Line: 10, Body: "nit", Resolved: false, CreatedAt: now},
		{CommentID: "c2", Author: "bob", File: "b.go", Line: 20, Body: "bug", Resolved: true, CreatedAt: now.Add(time.Second)},
	}
	if err := s.ReplacePRComments(ctx, "s1", comments); err != nil {
		t.Fatalf("replace comments: %v", err)
	}
	gotComments, err := s.ListPRComments(ctx, "s1")
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if !reflect.DeepEqual(gotComments, comments) {
		t.Fatalf("comments = %+v, want %+v", gotComments, comments)
	}

	// Deleting the pr cascades its checks and comments.
	if err := s.DeletePR(ctx, "s1"); err != nil {
		t.Fatalf("delete pr: %v", err)
	}
	if c, _ := s.ListPRChecks(ctx, "s1"); len(c) != 0 {
		t.Fatalf("checks not cascaded: %+v", c)
	}
	if c, _ := s.ListPRComments(ctx, "s1"); len(c) != 0 {
		t.Fatalf("comments not cascaded: %+v", c)
	}
}
