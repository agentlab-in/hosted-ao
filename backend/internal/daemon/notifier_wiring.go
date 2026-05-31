package daemon

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/notification"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type notifierStack struct {
	Manager *notification.Manager
	done    <-chan struct{}
}

func startNotifier(ctx context.Context, cfg config.Config, store *sqlite.Store, log *slog.Logger) *notifierStack {
	mgr := notification.NewManager(store, notification.SettingsFromConfig(cfg), log)
	done := mgr.Start(ctx)
	return &notifierStack{Manager: mgr, done: done}
}

func (s *notifierStack) Stop() {
	if s == nil || s.done == nil {
		return
	}
	<-s.done
}
