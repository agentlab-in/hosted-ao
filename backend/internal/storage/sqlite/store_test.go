package sqlite

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func sampleRecord(id string) domain.SessionRecord {
	now := time.Now().UTC().Truncate(time.Second)
	return domain.SessionRecord{
		ID:        domain.SessionID(id),
		ProjectID: "proj",
		IssueID:   "issue-1",
		Kind:      domain.KindWorker,
		CreatedAt: now,
		UpdatedAt: now,
		Lifecycle: domain.CanonicalSessionLifecycle{
			Session:  domain.SessionSubstate{State: domain.SessionWorking, Reason: domain.ReasonTaskInProgress},
			PR:       domain.PRSubstate{State: domain.PRNone, Reason: domain.PRReasonNotCreated},
			Runtime:  domain.RuntimeSubstate{State: domain.RuntimeAlive, Reason: domain.RuntimeReasonProcessRunning},
			Activity: domain.ActivitySubstate{State: domain.ActivityActive, LastActivityAt: now, Source: domain.SourceNative},
		},
	}
}

func TestUpsertInsertThenUpdateBumpsRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := sampleRecord("s1")

	if err := s.Upsert(ctx, rec, ports.EventSessionCreated); err != nil {
		t.Fatalf("insert: %v", err)
	}
	lc, ok, err := s.Load(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("load after insert: ok=%v err=%v", ok, err)
	}
	if lc.Revision != 1 {
		t.Fatalf("revision after insert = %d, want 1", lc.Revision)
	}

	// Update must carry the loaded revision (1) and persist as 2.
	rec.Lifecycle.Revision = 1
	rec.Lifecycle.Session.State = domain.SessionIdle
	if err := s.Upsert(ctx, rec, ports.EventSessionStateChanged); err != nil {
		t.Fatalf("update: %v", err)
	}
	lc, _, _ = s.Load(ctx, "s1")
	if lc.Revision != 2 {
		t.Fatalf("revision after update = %d, want 2", lc.Revision)
	}
	if lc.Session.State != domain.SessionIdle {
		t.Fatalf("state after update = %q, want idle", lc.Session.State)
	}
}

func TestUpsertStaleRevisionMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := sampleRecord("s1")
	if err := s.Upsert(ctx, rec, ports.EventSessionCreated); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Stored revision is 1; submitting revision 0 (stale) must mismatch and
	// write nothing new (no extra outbox/change_log rows).
	rec.Lifecycle.Revision = 0
	err := s.Upsert(ctx, rec, ports.EventSessionStateChanged)
	if err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("stale update err = %v, want revision mismatch", err)
	}
	assertOutboxCount(t, s, ctx, 1)
}

func TestUpsertInsertNonZeroRevisionErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := sampleRecord("s1")
	rec.Lifecycle.Revision = 5
	err := s.Upsert(ctx, rec, ports.EventSessionCreated)
	if err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("insert with revision 5 err = %v, want revision mismatch", err)
	}
	// Nothing should be persisted.
	if _, ok, _ := s.Get(ctx, "s1"); ok {
		t.Fatal("session persisted despite revision-mismatch insert")
	}
	assertOutboxCount(t, s, ctx, 0)
}

func TestUpsertOutboxAtomicityAndOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := sampleRecord("s1")
	if err := s.Upsert(ctx, rec, ports.EventSessionCreated); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rec.Lifecycle.Revision = 1
	if err := s.Upsert(ctx, rec, ports.EventSessionStateChanged); err != nil {
		t.Fatalf("update: %v", err)
	}

	rows, err := NewStore(s.db).q.ListUnsentOutbox(ctx, 100)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2", len(rows))
	}
	// seq strictly monotonic, event types verbatim, revisions 1 then 2.
	if rows[0].ChangeLogSeq != 1 || rows[1].ChangeLogSeq != 2 {
		t.Fatalf("seq not monotonic: %d, %d", rows[0].ChangeLogSeq, rows[1].ChangeLogSeq)
	}
	if rows[0].EventType != string(ports.EventSessionCreated) || rows[1].EventType != string(ports.EventSessionStateChanged) {
		t.Fatalf("event types = %q, %q", rows[0].EventType, rows[1].EventType)
	}
	if rows[0].Revision != 1 || rows[1].Revision != 2 {
		t.Fatalf("revisions = %d, %d, want 1, 2", rows[0].Revision, rows[1].Revision)
	}
}

func TestGetListRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := sampleRecord("a")
	b := sampleRecord("b")
	b.ProjectID = "other"
	if err := s.Upsert(ctx, a, ports.EventSessionCreated); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, b, ports.EventSessionCreated); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Get(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("get a: ok=%v err=%v", ok, err)
	}
	if got.ID != "a" || got.Lifecycle.Revision != 1 || got.IssueID != "issue-1" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if !got.Metadata.IsZero() {
		t.Fatalf("Get must not reconstruct metadata, got %v", got.Metadata)
	}

	list, err := s.List(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "a" {
		t.Fatalf("List(proj) = %+v, want only a", list)
	}
}

func TestMetadataSideChannel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, sampleRecord("s1"), ports.EventSessionCreated); err != nil {
		t.Fatal(err)
	}

	if err := s.PatchMetadata(ctx, "s1", domain.SessionMetadata{Branch: "feat/x", Prompt: "do it"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	// A partial patch (only Branch) must not clobber the earlier Prompt.
	if err := s.PatchMetadata(ctx, "s1", domain.SessionMetadata{Branch: "feat/y"}); err != nil {
		t.Fatalf("patch overwrite: %v", err)
	}

	m, err := s.GetMetadata(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Branch != "feat/y" || m.Prompt != "do it" {
		t.Fatalf("metadata = %+v", m)
	}
	// Metadata writes must not bump revision (off the canonical path).
	lc, _, _ := s.Load(ctx, "s1")
	if lc.Revision != 1 {
		t.Fatalf("revision = %d after metadata patch, want 1 (no bump)", lc.Revision)
	}
}

func TestDetectingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := sampleRecord("s1")
	rec.Lifecycle.Session.State = domain.SessionDetecting
	rec.Lifecycle.Detecting = &domain.DetectingState{
		Attempts:     2,
		StartedAt:    time.Now().UTC().Truncate(time.Second),
		EvidenceHash: "abc123",
	}
	if err := s.Upsert(ctx, rec, ports.EventSessionCreated); err != nil {
		t.Fatal(err)
	}
	lc, _, _ := s.Load(ctx, "s1")
	if lc.Detecting == nil {
		t.Fatal("Detecting lost on round-trip")
	}
	if lc.Detecting.Attempts != 2 || lc.Detecting.EvidenceHash != "abc123" {
		t.Fatalf("detecting = %+v", lc.Detecting)
	}

	// Clearing Detecting must null the columns back out.
	rec.Lifecycle.Revision = 1
	rec.Lifecycle.Detecting = nil
	if err := s.Upsert(ctx, rec, ports.EventSessionStateChanged); err != nil {
		t.Fatal(err)
	}
	lc, _, _ = s.Load(ctx, "s1")
	if lc.Detecting != nil {
		t.Fatalf("Detecting not cleared: %+v", lc.Detecting)
	}
}

func TestLoadGetMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, ok, err := s.Load(ctx, "nope"); ok || err != nil {
		t.Fatalf("Load missing: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Get(ctx, "nope"); ok || err != nil {
		t.Fatalf("Get missing: ok=%v err=%v", ok, err)
	}
	if m, err := s.GetMetadata(ctx, "nope"); err != nil || !m.IsZero() {
		t.Fatalf("GetMetadata missing: m=%v err=%v", m, err)
	}
}

func assertOutboxCount(t *testing.T, s *Store, ctx context.Context, want int) {
	t.Helper()
	rows, err := s.q.ListUnsentOutbox(ctx, 1000)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(rows) != want {
		t.Fatalf("outbox count = %d, want %d", len(rows), want)
	}
}

// TestConcurrentReadsAndWrites exercises the read-pool + write-mutex model:
// many writers (each its own session) run alongside many readers hammering
// ListAll. Reads must not be serialized behind writes, writes must not corrupt
// or error under the revision-CAS, and the final state must be exact. Run under
// -race this also guards the writeMu discipline.
func TestConcurrentReadsAndWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const n = 16

	var wg sync.WaitGroup
	errc := make(chan error, n*2)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Upsert(ctx, sampleRecord(fmt.Sprintf("s%02d", i)), ports.EventSessionCreated); err != nil {
				errc <- err
			}
		}(i)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := s.ListAll(ctx); err != nil {
					errc <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatalf("concurrent op error: %v", err)
	}

	got, err := s.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("after %d concurrent inserts, ListAll returned %d", n, len(got))
	}
}
