package notification

import (
	"context"
	"time"
)

func startDispatcher(ctx context.Context, m *Manager) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDispatcherOnce(ctx, m)

		interval := m.interval
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runDispatcherOnce(ctx, m)
			}
		}
	}()
	return done
}

func runDispatcherOnce(ctx context.Context, m *Manager) {
	if err := m.RunOnce(ctx); err != nil {
		m.logger.ErrorContext(ctx, "notification dispatcher tick", "err", err)
	}
}
