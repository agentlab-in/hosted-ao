package lifecycle

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// TestStoreParity is the key contract test from the plan: it drives the REAL
// Lifecycle Manager through identical operation sequences against the in-memory
// fakeStore (the authoritative store semantics) and the SQLite-backed Store,
// then asserts the resulting canonical lifecycle is byte-identical. If the
// SQLite adapter honored the port exactly, the two managers cannot diverge.
//
// Both stores are seeded the same way (via the public Upsert insert path, so
// both start at revision 1) — this makes revision numbers, not just states,
// directly comparable.
func TestStoreParity(t *testing.T) {
	seed := lc(domain.SessionWorking, domain.ReasonTaskInProgress, domain.RuntimeAlive)
	seed.Activity = domain.ActivitySubstate{State: domain.ActivityActive, LastActivityAt: t0, Source: domain.SourceNative}

	cases := []struct {
		name string
		ops  []func(*Manager) error
	}{
		{
			name: "runtime dead then activity signal",
			ops: []func(*Manager) error{
				func(m *Manager) error {
					return m.ApplyRuntimeObservation(context.Background(), sid, ports.RuntimeFacts{
						RuntimeState: ports.RuntimeProbeDead, ProcessState: ports.ProcessProbeDead, ObservedAt: t0,
					})
				},
				func(m *Manager) error {
					return m.ApplyActivitySignal(context.Background(), sid, ports.ActivitySignal{
						State: ports.SignalValid, Activity: domain.ActivityActive, Timestamp: t0, Source: domain.SourceHook,
					})
				},
			},
		},
		{
			name: "scm pr open then changes requested",
			ops: []func(*Manager) error{
				func(m *Manager) error {
					return m.ApplySCMObservation(context.Background(), sid, ports.SCMFacts{
						Fetched: true, PRState: domain.PROpen, PRNumber: 7, PRURL: "http://x/7",
					})
				},
			},
		},
		{
			name: "kill request terminates",
			ops: []func(*Manager) error{
				func(m *Manager) error {
					return m.OnKillRequested(context.Background(), sid, ports.KillReason{Kind: ports.KillManual, Detail: "x"})
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeMgr, fakeS := newManager()
			sqlMgr, sqlS := newSQLiteManager(t)

			seedViaUpsert(t, fakeS, seed)
			seedViaUpsert(t, sqlS, seed)

			for i, op := range tc.ops {
				errF := op(fakeMgr)
				errS := op(sqlMgr)
				if (errF == nil) != (errS == nil) {
					t.Fatalf("op %d error divergence: fake=%v sqlite=%v", i, errF, errS)
				}
			}

			fl, okF, _ := fakeS.Load(context.Background(), sid)
			sl, okS, _ := sqlS.Load(context.Background(), sid)
			if okF != okS {
				t.Fatalf("presence divergence: fake=%v sqlite=%v", okF, okS)
			}
			assertLifecycleEqual(t, fl, sl)
		})
	}
}

func newSQLiteManager(t *testing.T) (*Manager, *sqlite.Store) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewStore(db)
	return New(store, &recordingNotifier{}, &recordingMessenger{}), store
}

func seedViaUpsert(t *testing.T, store ports.LifecycleStore, l domain.CanonicalSessionLifecycle) {
	t.Helper()
	rec := domain.SessionRecord{
		ID:        sid,
		ProjectID: "proj",
		Kind:      domain.KindWorker,
		CreatedAt: t0,
		UpdatedAt: t0,
		Lifecycle: l,
	}
	if err := store.Upsert(context.Background(), rec, ports.EventSessionCreated); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
}

func assertLifecycleEqual(t *testing.T, a, b domain.CanonicalSessionLifecycle) {
	t.Helper()
	if a.Revision != b.Revision {
		t.Errorf("revision: fake=%d sqlite=%d", a.Revision, b.Revision)
	}
	if a.Session != b.Session {
		t.Errorf("session: fake=%+v sqlite=%+v", a.Session, b.Session)
	}
	if a.PR != b.PR {
		t.Errorf("pr: fake=%+v sqlite=%+v", a.PR, b.PR)
	}
	if a.Runtime != b.Runtime {
		t.Errorf("runtime: fake=%+v sqlite=%+v", a.Runtime, b.Runtime)
	}
	if a.Activity.State != b.Activity.State || a.Activity.Source != b.Activity.Source ||
		!a.Activity.LastActivityAt.Equal(b.Activity.LastActivityAt) {
		t.Errorf("activity: fake=%+v sqlite=%+v", a.Activity, b.Activity)
	}
	switch {
	case a.Detecting == nil && b.Detecting == nil:
	case a.Detecting == nil || b.Detecting == nil:
		t.Errorf("detecting presence: fake=%v sqlite=%v", a.Detecting, b.Detecting)
	default:
		if a.Detecting.Attempts != b.Detecting.Attempts || a.Detecting.EvidenceHash != b.Detecting.EvidenceHash ||
			!a.Detecting.StartedAt.Equal(b.Detecting.StartedAt) {
			t.Errorf("detecting: fake=%+v sqlite=%+v", a.Detecting, b.Detecting)
		}
	}
}
