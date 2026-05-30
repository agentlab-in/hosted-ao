package lifecycle

import (
	"context"
	"fmt"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeStore is an in-memory LifecycleStore that faithfully applies full-row
// Upsert semantics so tests assert against the real persisted canonical.
type fakeStore struct {
	mu       sync.Mutex
	records  map[domain.SessionID]*domain.SessionRecord
	metadata map[domain.SessionID]domain.SessionMetadata
}

var _ ports.LifecycleStore = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		records:  map[domain.SessionID]*domain.SessionRecord{},
		metadata: map[domain.SessionID]domain.SessionMetadata{},
	}
}

// seed installs a starting lifecycle for a session id (bypassing the patch path).
func (s *fakeStore) seed(id domain.SessionID, l domain.CanonicalSessionLifecycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.Version == 0 {
		l.Version = domain.LifecycleVersion
	}
	s.records[id] = &domain.SessionRecord{ID: id, Lifecycle: l}
}

func (s *fakeStore) Load(_ context.Context, id domain.SessionID) (domain.CanonicalSessionLifecycle, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return domain.CanonicalSessionLifecycle{}, false, nil
	}
	return rec.Lifecycle, true, nil
}

func (s *fakeStore) Upsert(_ context.Context, rec domain.SessionRecord, _ ports.EventType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[rec.ID]; ok {
		if rec.Lifecycle.Revision != existing.Lifecycle.Revision {
			return fmt.Errorf("revision mismatch for %s: have %d, want %d", rec.ID, rec.Lifecycle.Revision, existing.Lifecycle.Revision)
		}
		rec.Lifecycle.Revision = existing.Lifecycle.Revision + 1
	} else {
		if rec.Lifecycle.Revision != 0 {
			return fmt.Errorf("revision mismatch for insert %s: have %d, want 0", rec.ID, rec.Lifecycle.Revision)
		}
		rec.Lifecycle.Revision = 1
	}
	if rec.Lifecycle.Version == 0 {
		rec.Lifecycle.Version = domain.LifecycleVersion
	}
	r := rec
	s.records[rec.ID] = &r
	return nil
}

func (s *fakeStore) Get(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return domain.SessionRecord{}, false, nil
	}
	return *rec, true, nil
}

func (s *fakeStore) List(_ context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.SessionRecord
	for _, rec := range s.records {
		if rec.ProjectID == project {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (s *fakeStore) GetMetadata(_ context.Context, id domain.SessionID) (domain.SessionMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata[id], nil
}

func (s *fakeStore) PatchMetadata(_ context.Context, id domain.SessionID, meta domain.SessionMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata[id] = mergeSessionMetadata(s.metadata[id], meta)
	return nil
}

// mergeSessionMetadata applies meta onto dst with the store's "empty = leave
// unchanged" semantics, so partial patches do not clobber earlier values.
func mergeSessionMetadata(dst, meta domain.SessionMetadata) domain.SessionMetadata {
	if meta.Branch != "" {
		dst.Branch = meta.Branch
	}
	if meta.WorkspacePath != "" {
		dst.WorkspacePath = meta.WorkspacePath
	}
	if meta.RuntimeHandleID != "" {
		dst.RuntimeHandleID = meta.RuntimeHandleID
	}
	if meta.RuntimeName != "" {
		dst.RuntimeName = meta.RuntimeName
	}
	if meta.AgentSessionID != "" {
		dst.AgentSessionID = meta.AgentSessionID
	}
	if meta.Prompt != "" {
		dst.Prompt = meta.Prompt
	}
	return dst
}

// recordingNotifier captures emitted events for assertions.
type recordingNotifier struct {
	mu     sync.Mutex
	events []ports.OrchestratorEvent
}

var _ ports.Notifier = (*recordingNotifier)(nil)

func (n *recordingNotifier) Notify(_ context.Context, e ports.OrchestratorEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, e)
	return nil
}

// recordingMessenger captures messages injected into agents.
type recordingMessenger struct {
	mu   sync.Mutex
	sent []struct {
		ID      domain.SessionID
		Message string
	}
}

var _ ports.AgentMessenger = (*recordingMessenger)(nil)

func (a *recordingMessenger) Send(_ context.Context, id domain.SessionID, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, struct {
		ID      domain.SessionID
		Message string
	}{id, message})
	return nil
}
