package main

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/reaper"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// lifecycleStack owns the running LCM + reaper. The LCM is the sole writer into
// the store (every Apply*/On* call ends in store.Upsert, which the CDC pipeline
// then drains); the reaper is the OBSERVE-layer timer that probes live runtimes
// and reports facts back through the LCM. Together with the CDC substrate this
// makes the write path live end-to-end: LCM -> store -> outbox -> JSONL ->
// broadcaster.
type lifecycleStack struct {
	LCM        *lifecycle.Manager
	reaperDone <-chan struct{}
}

// startLifecycle constructs the LCM over store, makes escalation budgets durable,
// teaches it to enumerate sessions for the reaper, and starts the reaper loop.
// The goroutine stops when ctx is cancelled; Stop waits for it to drain.
//
// TEMPORARY STUBS (replace as the daemon lane lands the real collaborators):
//
//   - noopNotifier — swap for the production notifier multiplexer once the
//     notifier plugins (desktop/Slack/webhook) are ported. Wire it where
//     noopNotifier{} is passed to lifecycle.New below.
//   - noopMessenger — swap for the AgentMessenger backed by the runtime/agent
//     plugins (it injects a prompt into the live agent pane). Wire it at the
//     same lifecycle.New call site.
//   - reaper.MapRegistry{} — empty runtime registry, so the reaper probes
//     nothing. Register the real runtime adapters (tmux/process) keyed by
//     runtime name once those plugins exist: reaper.MapRegistry{"tmux": rt}.
func startLifecycle(ctx context.Context, store *sqlite.Store, logger *slog.Logger) (*lifecycleStack, error) {
	// TODO(daemon-lane): replace noopNotifier{}/noopMessenger{} with the real
	// notifier multiplexer and the plugin-backed AgentMessenger.
	lcm := lifecycle.New(store, noopNotifier{}, noopMessenger{})

	// Durable escalation budgets (flaw #3 fix): hydrate from the store and turn
	// on write-through so a restart does not re-fire an already-escalated page.
	// Must run before the reaper starts dispatching TickEscalations.
	if err := lcm.WithReactionStore(ctx, lifecycleReactionStore{store}); err != nil {
		return nil, err
	}

	// The reaper's RunningSessions snapshot needs to see every session; ListAll
	// spans all projects (the per-project List would hide cross-project work).
	lcm.WithSessionLister(store.ListAll)

	// TODO(daemon-lane): pass the real runtime registry so the reaper actually
	// probes live panes. With an empty registry it ticks escalations but probes
	// nothing, which is correct until runtimes exist.
	rp := reaper.New(lcm, reaper.MapRegistry{}, reaper.Config{Logger: logger})

	return &lifecycleStack{LCM: lcm, reaperDone: rp.Start(ctx)}, nil
}

// Stop waits for the reaper goroutine to exit (the caller must have cancelled the
// ctx passed to startLifecycle).
func (l *lifecycleStack) Stop() {
	<-l.reaperDone
}

// noopNotifier satisfies ports.Notifier by dropping every event. TEMPORARY: the
// daemon lane replaces this with the notifier multiplexer over the real notifier
// plugins. Until then human-facing notifications are silently discarded — the
// write path and CDC still work, only the human push is absent.
type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, ports.OrchestratorEvent) error { return nil }

// noopMessenger satisfies ports.AgentMessenger by dropping every send. TEMPORARY:
// replace with the runtime/agent-plugin-backed messenger that injects prompts
// into the live agent pane. Until then auto-nudge reactions are no-ops.
type noopMessenger struct{}

func (noopMessenger) Send(context.Context, domain.SessionID, string) error { return nil }

// lifecycleReactionStore bridges the concrete *sqlite.Store to the lifecycle
// package's ReactionStore interface (string/row types <-> domain types). It is
// the production twin of the reactionStoreAdapter used in the lifecycle tests.
type lifecycleReactionStore struct{ store *sqlite.Store }

func (a lifecycleReactionStore) LoadReactionTrackers(ctx context.Context) ([]lifecycle.PersistedTracker, error) {
	rows, err := a.store.ListReactionTrackers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]lifecycle.PersistedTracker, len(rows))
	for i, r := range rows {
		out[i] = lifecycle.PersistedTracker{
			SessionID:      domain.SessionID(r.SessionID),
			Key:            r.ReactionKey,
			Attempts:       r.Attempts,
			Escalated:      r.Escalated,
			FirstAttemptAt: r.FirstAttemptAt,
			ProjectID:      domain.ProjectID(r.ProjectID),
		}
	}
	return out, nil
}

func (a lifecycleReactionStore) SaveReactionTracker(ctx context.Context, t lifecycle.PersistedTracker) error {
	return a.store.SaveReactionTracker(ctx, sqlite.ReactionTrackerRow{
		SessionID:      string(t.SessionID),
		ReactionKey:    t.Key,
		Attempts:       t.Attempts,
		Escalated:      t.Escalated,
		FirstAttemptAt: t.FirstAttemptAt,
		ProjectID:      string(t.ProjectID),
	})
}

func (a lifecycleReactionStore) DeleteReactionTracker(ctx context.Context, id domain.SessionID, key string) error {
	return a.store.DeleteReactionTracker(ctx, string(id), key)
}

func (a lifecycleReactionStore) DeleteSessionReactionTrackers(ctx context.Context, id domain.SessionID) error {
	return a.store.DeleteSessionReactionTrackers(ctx, string(id))
}
