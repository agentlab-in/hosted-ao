package notification

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeNotificationStore struct {
	row     domain.Notification
	created bool
}

func (f *fakeNotificationStore) EnqueueNotification(_ context.Context, row domain.Notification) (domain.Notification, bool, error) {
	f.row = row
	f.created = true
	return row, true, nil
}

func TestEnqueuerRendersAndPersists(t *testing.T) {
	store := &fakeNotificationStore{}
	renderer := NewRenderer(fakeReader{rec: renderRecord()})
	enq := NewEnqueuer(store, renderer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := enq.Notify(context.Background(), ports.Event{
		Type: "reaction.agent-needs-input", Priority: ports.PriorityUrgent,
		ProjectID: "ao", SessionID: "ao-7", Message: "needs input",
		Reaction: &ports.ReactionEvent{Key: "agent-needs-input", Action: "notify"},
	}); err != nil {
		t.Fatal(err)
	}
	if !store.created || store.row.SemanticType != "session.needs_input" || store.row.DedupeKey == "" {
		t.Fatalf("store row not rendered: created=%v row=%+v", store.created, store.row)
	}
}
