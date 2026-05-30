package cdc

import (
	"context"
	"log/slog"
	"time"
)

// DefaultJanitorInterval is the outbox-vacuum cadence.
const DefaultJanitorInterval = 60 * time.Second

// Vacuum is the janitor's view of storage: the safe deletion watermark and the
// delete itself.
type Vacuum interface {
	MinConsumerOffset(ctx context.Context) (int64, error)
	DeleteSentOutboxBelow(ctx context.Context, seq int64) (int64, error)
}

// Janitor reclaims delivered outbox rows every consumer has acknowledged.
//
// Watermark: MIN(consumer_offsets.last_seq). Rows with seq < watermark are sent
// AND past every consumer's cursor, so they are safe to drop. When the watermark
// is 0 (a consumer exists but has acknowledged nothing, or none is registered
// yet) the janitor deletes nothing — it never races ahead of a consumer that
// has not yet read an event. change_log is never touched: it is the durable
// history and the snapshot-resync floor.
type Janitor struct {
	store    Vacuum
	interval time.Duration
	logger   *slog.Logger
}

// JanitorConfig holds optional knobs; zero values fall back to defaults.
type JanitorConfig struct {
	Interval time.Duration
	Logger   *slog.Logger
}

// NewJanitor constructs a Janitor over store.
func NewJanitor(store Vacuum, cfg JanitorConfig) *Janitor {
	j := &Janitor{store: store, interval: cfg.Interval, logger: cfg.Logger}
	if j.interval <= 0 {
		j.interval = DefaultJanitorInterval
	}
	if j.logger == nil {
		j.logger = slog.Default()
	}
	return j
}

// Start runs the vacuum loop until ctx is cancelled; the returned channel closes
// when the loop has exited.
func (j *Janitor) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(j.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := j.Sweep(ctx); err != nil {
					j.logger.Error("cdc janitor: sweep failed", "err", err)
				}
			}
		}
	}()
	return done
}

// Sweep deletes delivered outbox rows below the safe watermark and returns the
// number removed.
func (j *Janitor) Sweep(ctx context.Context) (int64, error) {
	watermark, err := j.store.MinConsumerOffset(ctx)
	if err != nil {
		return 0, err
	}
	if watermark <= 0 {
		return 0, nil
	}
	return j.store.DeleteSentOutboxBelow(ctx, watermark)
}
