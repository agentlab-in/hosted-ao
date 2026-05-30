package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// cdcConsumerName is the durable consumer_offsets key for the in-process FE
// broadcast consumer. A second transport (e.g. a cloud relay) would use its own
// key so each tracks an independent cursor.
const cdcConsumerName = "fe-broadcast"

// cdcPipeline owns the running CDC goroutines and the broadcaster the FE
// transport subscribes to. It is the durable change-delivery substrate: the
// publisher drains the outbox to JSONL, the consumer tails the log and fans out
// through the broadcaster, and the janitor reclaims acknowledged outbox rows.
type cdcPipeline struct {
	Broadcaster *cdc.Broadcaster
	log         *cdc.Log
	dones       []<-chan struct{}
}

// startCDC opens the JSONL log and starts the publisher, consumer, and janitor
// against store, returning a handle whose Stop waits for the goroutines to
// drain after ctx is cancelled. The goroutines stop when ctx is cancelled.
func startCDC(ctx context.Context, store *sqlite.Store, dataDir string, logger *slog.Logger) (*cdcPipeline, error) {
	log, err := cdc.OpenLog(dataDir, 0)
	if err != nil {
		return nil, fmt.Errorf("open cdc log: %w", err)
	}

	bcast := cdc.NewBroadcaster()
	logPath := filepath.Join(dataDir, cdc.LogFileName)

	pub := cdc.NewPublisher(outboxAdapter{store}, log, cdc.PublisherConfig{Logger: logger})
	con := cdc.NewConsumer(cdcConsumerName, logPath, store, bcast, cdc.ConsumerConfig{
		Snapshot: snapshotSource{store},
		Logger:   logger,
	})
	jan := cdc.NewJanitor(store, cdc.JanitorConfig{Logger: logger})

	conDone, err := con.Start(ctx)
	if err != nil {
		log.Close()
		return nil, fmt.Errorf("start cdc consumer: %w", err)
	}

	return &cdcPipeline{
		Broadcaster: bcast,
		log:         log,
		dones:       []<-chan struct{}{pub.Start(ctx), conDone, jan.Start(ctx)},
	}, nil
}

// Stop waits for every CDC goroutine to exit (the caller must have cancelled the
// ctx passed to startCDC) and closes the log file.
func (p *cdcPipeline) Stop() error {
	for _, d := range p.dones {
		<-d
	}
	return p.log.Close()
}

// outboxAdapter bridges *sqlite.Store's outbox methods to cdc.OutboxStore,
// mapping the storage-native OutboxEvent to the transport's PendingEvent. (The
// offset and vacuum contracts need no adapter — *sqlite.Store satisfies
// cdc.OffsetStore and cdc.Vacuum directly.)
type outboxAdapter struct{ store *sqlite.Store }

func (a outboxAdapter) ListUnsent(ctx context.Context, limit int) ([]cdc.PendingEvent, error) {
	evs, err := a.store.ListUnsent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]cdc.PendingEvent, len(evs))
	for i, e := range evs {
		out[i] = cdc.PendingEvent{
			OutboxID: e.OutboxID,
			Event: cdc.Event{
				Seq:       e.Seq,
				SessionID: e.SessionID,
				EventType: e.EventType,
				Revision:  e.Revision,
				Payload:   e.Payload,
				CreatedAt: e.CreatedAt,
			},
		}
	}
	return out, nil
}

func (a outboxAdapter) MarkSent(ctx context.Context, id int64, at time.Time) error {
	return a.store.MarkSent(ctx, id, at)
}

func (a outboxAdapter) MarkFailed(ctx context.Context, id int64, msg string) error {
	return a.store.MarkFailed(ctx, id, msg)
}

// snapshotSource rebuilds current state from the sessions table after a
// log-rotation gap, emitting one full-state event per session. Each event
// carries the change_log high-water seq so the consumer resumes its cursor
// there; the payload mirrors the canonical change_log payload (metadata
// excluded, version stamped) so subscribers parse snapshot and live events the
// same way.
type snapshotSource struct{ store *sqlite.Store }

func (s snapshotSource) Snapshot(ctx context.Context) ([]cdc.Event, int64, error) {
	recs, err := s.store.ListAll(ctx)
	if err != nil {
		return nil, 0, err
	}
	maxSeq, err := s.store.MaxChangeLogSeq(ctx)
	if err != nil {
		return nil, 0, err
	}
	events := make([]cdc.Event, 0, len(recs))
	for _, r := range recs {
		r.Lifecycle.Version = domain.LifecycleVersion
		blob, err := json.Marshal(r)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal snapshot %s: %w", r.ID, err)
		}
		events = append(events, cdc.Event{
			Seq:       maxSeq,
			SessionID: string(r.ID),
			EventType: "session_snapshot",
			Revision:  int64(r.Lifecycle.Revision),
			Payload:   string(blob),
			CreatedAt: r.UpdatedAt,
		})
	}
	return events, maxSeq, nil
}
