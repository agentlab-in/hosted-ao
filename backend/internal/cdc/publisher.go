package cdc

import (
	"context"
	"log/slog"
	"time"
)

// DefaultPublishInterval is the outbox drain cadence.
const DefaultPublishInterval = 50 * time.Millisecond

// DefaultBatchSize bounds how many outbox rows one drain pass handles.
const DefaultBatchSize = 256

// PendingEvent is an undelivered outbox row paired with its CDC event payload.
type PendingEvent struct {
	OutboxID int64
	Event
}

// OutboxStore is the publisher's view of the storage layer: read undelivered
// rows in seq order, then mark each delivered or failed.
type OutboxStore interface {
	ListUnsent(ctx context.Context, limit int) ([]PendingEvent, error)
	MarkSent(ctx context.Context, outboxID int64, at time.Time) error
	MarkFailed(ctx context.Context, outboxID int64, errMsg string) error
}

// Publisher drains the outbox into the JSONL log on a fixed cadence.
type Publisher struct {
	src      OutboxStore
	log      *Log
	interval time.Duration
	batch    int
	clock    func() time.Time
	logger   *slog.Logger
}

// PublisherConfig holds optional knobs; zero values fall back to defaults.
type PublisherConfig struct {
	Interval time.Duration
	Batch    int
	Clock    func() time.Time
	Logger   *slog.Logger
}

// NewPublisher constructs a Publisher over src and log.
func NewPublisher(src OutboxStore, log *Log, cfg PublisherConfig) *Publisher {
	p := &Publisher{
		src:      src,
		log:      log,
		interval: cfg.Interval,
		batch:    cfg.Batch,
		clock:    cfg.Clock,
		logger:   cfg.Logger,
	}
	if p.interval <= 0 {
		p.interval = DefaultPublishInterval
	}
	if p.batch <= 0 {
		p.batch = DefaultBatchSize
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	return p
}

// Start runs the drain loop until ctx is cancelled; the returned channel closes
// when the loop has exited.
func (p *Publisher) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.Drain(ctx); err != nil {
					p.logger.Error("cdc publisher: drain failed", "err", err)
				}
			}
		}
	}()
	return done
}

// Drain runs one pass: append each undelivered row to the log in seq order,
// marking it sent. A write failure stops the pass (the row is marked failed and
// retried next tick) so ordering is never violated by skipping ahead.
func (p *Publisher) Drain(ctx context.Context) error {
	pending, err := p.src.ListUnsent(ctx, p.batch)
	if err != nil {
		return err
	}
	for _, pe := range pending {
		if err := p.log.Append(pe.Event); err != nil {
			p.logger.Error("cdc publisher: append failed", "outboxId", pe.OutboxID, "seq", pe.Seq, "err", err)
			if merr := p.src.MarkFailed(ctx, pe.OutboxID, err.Error()); merr != nil {
				p.logger.Error("cdc publisher: mark failed errored", "outboxId", pe.OutboxID, "err", merr)
			}
			return nil
		}
		if err := p.src.MarkSent(ctx, pe.OutboxID, p.clock().UTC()); err != nil {
			return err
		}
	}
	return nil
}
