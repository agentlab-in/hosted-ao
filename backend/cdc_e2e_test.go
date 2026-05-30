package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These are full-stack end-to-end tests of the write+delivery path wired exactly
// as main.go wires it: real sqlite.Store -> real outboxAdapter -> real
// cdc.Publisher -> real JSONL log -> real cdc.Consumer -> real cdc.Broadcaster,
// using the REAL snapshotSource (store.ListAll) rather than a fake. The cdc
// package's own integration test covers the synchronous Drain/Poll happy path
// with a fake snapshot; these cover the two gaps it leaves: a rotation that
// resyncs from the actual sessions table, and the concurrent goroutine model
// the daemon actually runs.

// TestE2E_RealSnapshotResyncThroughRotation forces a log rotation and asserts the
// consumer rebuilds state from the REAL sessions-table snapshot (not the
// rotated-away bytes), delivering the persisted record's payload.
func TestE2E_RealSnapshotResyncThroughRotation(t *testing.T) {
	ctx := context.Background()
	store := newWiringStore(t)
	dir := t.TempDir()
	log, err := cdc.OpenLog(dir, 80) // tiny cap: the second write forces a rotation
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var mu sync.Mutex
	var got []cdc.Event
	bc := cdc.NewBroadcaster()
	bc.Subscribe(func(e cdc.Event) { mu.Lock(); got = append(got, e); mu.Unlock() })

	con := cdc.NewConsumer("fe", filepath.Join(dir, cdc.LogFileName), store, bc,
		cdc.ConsumerConfig{Snapshot: snapshotSource{store: store}})
	if _, err := con.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pub := cdc.NewPublisher(outboxAdapter{store: store}, log, cdc.PublisherConfig{})

	// First canonical write: drained and consumed live from the original file.
	if err := store.Upsert(ctx, wiringRec("s1"), ports.EventSessionCreated); err != nil {
		t.Fatal(err)
	}
	if err := pub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := con.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(got)
	mu.Unlock()

	// Second write pushes the log past its cap -> rotation. The consumer sees a
	// fresh file and must resync from the sessions table.
	r := wiringRec("s1")
	r.Lifecycle.Revision = 1
	if err := store.Upsert(ctx, r, ports.EventSessionStateChanged); err != nil {
		t.Fatal(err)
	}
	if err := pub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := con.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) <= before {
		t.Fatalf("resync delivered nothing after rotation (got %d, before %d)", len(got), before)
	}
	// A real session_snapshot for s1 must have been delivered, carrying the full
	// record persisted in the sessions table.
	var snap *cdc.Event
	for i := range got {
		if got[i].EventType == "session_snapshot" && got[i].SessionID == "s1" {
			snap = &got[i]
		}
	}
	if snap == nil {
		t.Fatalf("no real session_snapshot delivered after rotation; got %+v", got)
	}
	var rec domain.SessionRecord
	if err := json.Unmarshal([]byte(snap.Payload), &rec); err != nil {
		t.Fatalf("snapshot payload not a SessionRecord: %v", err)
	}
	if rec.ID != "s1" || rec.Lifecycle.Session.State != domain.SessionWorking {
		t.Fatalf("snapshot payload mismatch: %+v", rec)
	}
	// The consumer's durable offset advanced to the change_log head.
	off, err := store.GetOffset(ctx, "fe")
	if err != nil {
		t.Fatal(err)
	}
	maxSeq, err := store.MaxChangeLogSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if off != maxSeq {
		t.Fatalf("offset = %d, want change_log head %d", off, maxSeq)
	}
}

// TestE2E_ConcurrentPublisherConsumer runs the publisher and consumer as the
// daemon runs them — independent goroutines on their own tickers — and asserts
// every canonical write is delivered exactly once, in order, with the offset
// landing at the head. Run under -race this also guards the broadcaster/consumer
// hand-off.
func TestE2E_ConcurrentPublisherConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newWiringStore(t)
	dir := t.TempDir()
	log, err := cdc.OpenLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var mu sync.Mutex
	var got []cdc.Event
	bc := cdc.NewBroadcaster()
	bc.Subscribe(func(e cdc.Event) { mu.Lock(); got = append(got, e); mu.Unlock() })

	pub := cdc.NewPublisher(outboxAdapter{store: store}, log, cdc.PublisherConfig{})
	con := cdc.NewConsumer("fe", filepath.Join(dir, cdc.LogFileName), store, bc, cdc.ConsumerConfig{})

	pubDone := pub.Start(ctx)
	conDone, err := con.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}

	const n = 5
	for i := 0; i < n; i++ {
		r := wiringRec("s1")
		r.Lifecycle.Revision = i
		evt := ports.EventSessionStateChanged
		if i == 0 {
			evt = ports.EventSessionCreated
		}
		if err := store.Upsert(ctx, r, evt); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// Bounded wait for the goroutine pipeline to deliver everything.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		count := len(got)
		mu.Unlock()
		if count >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: delivered %d/%d events", count, n)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-pubDone
	<-conDone

	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("delivered %d events, want %d", len(got), n)
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d (out-of-order or duplicate)", i, e.Seq, i+1)
		}
	}
	off, err := store.GetOffset(context.Background(), "fe")
	if err != nil {
		t.Fatal(err)
	}
	if off != n {
		t.Fatalf("offset = %d, want %d", off, n)
	}
}
