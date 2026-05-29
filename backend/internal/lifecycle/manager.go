// Package lifecycle implements ports.LifecycleManager: the synchronous
// observe->decide->persist reducer. Every Apply*/On* entrypoint runs the same
// pipeline under a per-session lock — load the full canonical record, run the
// matching pure decider, diff into a full-row Upsert, persist. The LCM never
// polls and never writes the display status (that is derived on read).
//
// After a transition is persisted, the Apply* paths fire the mapped reaction
// (the ACT layer: reaction table + escalation engine) via the react() chokepoint
// in reactions.go.
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain/decide"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Metadata keys OnSpawnCompleted records for the spawned session's handles.
const (
	MetaBranch          = "branch"
	MetaWorkspacePath   = "workspacePath"
	MetaRuntimeHandleID = "runtimeHandleId"
	MetaRuntimeName     = "runtimeName"
	MetaAgentSessionID  = "agentSessionId"
)

// Manager is the LCM. The Apply* pipeline persists a transition and then fires
// the mapped reaction via Notifier/AgentMessenger (see reactions.go).
type Manager struct {
	store     ports.LifecycleStore
	notifier  ports.Notifier
	messenger ports.AgentMessenger

	recentActivityWindow time.Duration
	locks                keyedMutex

	// trackers hold per-(session,reaction) escalation budgets (ACT policy, not
	// canonical state). trackerMu guards them: react() touches them from the
	// caller's goroutine, TickEscalations from the reaper's. clock is the time
	// source for escalation stamping (overridable in tests).
	trackers  map[trackerKey]*reactionTracker
	trackerMu sync.Mutex
	clock     func() time.Time
}

var _ ports.LifecycleManager = (*Manager)(nil)

func New(store ports.LifecycleStore, notifier ports.Notifier, messenger ports.AgentMessenger) *Manager {
	return &Manager{
		store:                store,
		notifier:             notifier,
		messenger:            messenger,
		recentActivityWindow: defaultRecentActivityWindow,
		trackers:             map[trackerKey]*reactionTracker{},
		clock:                time.Now,
	}
}

// ---- per-session serialisation ----

// keyedMutex hands out one lock per session id so the load->decide->persist
// read-modify-write is serial within a session but parallel across sessions.
//
// Entries are reference-counted and evicted when the last holder releases, so
// the map stays bounded to sessions with in-flight operations rather than
// growing unbounded over the lifetime of a long-running daemon.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[domain.SessionID]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) lock(id domain.SessionID) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[domain.SessionID]*lockEntry)
	}
	e, ok := k.locks[id]
	if !ok {
		e = &lockEntry{}
		k.locks[id] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.locks, id)
		}
		k.mu.Unlock()
	}
}

func (m *Manager) withLock(id domain.SessionID, fn func() error) error {
	unlock := m.locks.lock(id)
	defer unlock()
	return fn()
}

// transition is what a persisted write produced: the canonical before and after
// the full-row upsert. The ACT layer (react) derives the reaction from these. It
// is nil when the pipeline made no write.
type transition struct {
	beforeLC domain.CanonicalSessionLifecycle
	afterLC  domain.CanonicalSessionLifecycle
}

// mutate runs the shared pipeline: load full row -> build next canonical ->
// Upsert full row (only if changed). decideFn returns the full next lifecycle
// and whether it changed anything; false is a clean no-op (no write, no revision
// bump), which is how failed-probe / unknown-fact inputs are dropped.
//
// On a write it returns the transition (before/after canonical) so the caller —
// which still holds the originating facts — can fire the mapped reaction.
func (m *Manager) mutate(
	ctx context.Context,
	id domain.SessionID,
	decideFn func(cur domain.CanonicalSessionLifecycle, exists bool) (domain.CanonicalSessionLifecycle, bool, error),
) (*transition, error) {
	var tr *transition
	err := m.withLock(id, func() error {
		rec, exists, err := m.store.Get(ctx, id)
		if err != nil {
			return err
		}
		cur := rec.Lifecycle
		next, changed, err := decideFn(cur, exists)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		rec.Lifecycle = m.prepareLifecycleWrite(cur, next)
		rec.UpdatedAt = m.clock()
		if err := m.store.Upsert(ctx, rec); err != nil {
			return err
		}
		tr = &transition{beforeLC: cur, afterLC: rec.Lifecycle}
		return nil
	})
	return tr, err
}

func (m *Manager) prepareLifecycleWrite(cur, next domain.CanonicalSessionLifecycle) domain.CanonicalSessionLifecycle {
	next.Version = domain.LifecycleVersion
	next.Revision = cur.Revision + 1
	return next
}

// ---- OBSERVE entrypoints ----

// ApplyRuntimeObservation feeds the probe decider. Liveness always writes the
// runtime axis; the session axis follows the #1 composition rule; and a
// non-detecting verdict clears any stale detecting memory (#3) so the next
// probe doesn't read a phantom prior.
func (m *Manager) ApplyRuntimeObservation(ctx context.Context, id domain.SessionID, f ports.RuntimeFacts) error {
	tr, err := m.mutate(ctx, id, func(cur domain.CanonicalSessionLifecycle, exists bool) (domain.CanonicalSessionLifecycle, bool, error) {
		if !exists {
			return cur, false, nil // nothing seeded; ignore stray probe
		}

		d := decide.ResolveProbeDecision(runtimeFactsToProbeInput(f, cur, m.recentActivityWindow))

		next := cur
		changed := false

		if rt := runtimeSubstateFromFacts(f); cur.Runtime != rt {
			next.Runtime = rt
			changed = true
		}
		// A terminal session is reopened only by an explicit Restore: an
		// observation may refresh the runtime axis above but must touch neither
		// the session axis nor the detecting memory.
		if !isTerminal(cur.Session.State) {
			if shouldWriteSessionRuntime(d, cur) {
				changed = setSessionIfChanged(&next, d.SessionState, d.SessionReason) || changed
			}
			changed = setDetecting(&next, d.Detecting) || changed
		}

		return next, changed, nil
	})
	if err != nil {
		return err
	}
	return m.react(ctx, id, tr, reactionContext{})
}

// ApplySCMObservation maps PR facts onto the PR axis. A failed fetch is dropped
// (failed probe != "no PR"). An open or draft PR writes only the PR sub-state —
// the session axis stays owned by activity, and DeriveLegacyStatus surfaces the
// PR reason for display. A terminal PR (merged/closed) also parks the session.
func (m *Manager) ApplySCMObservation(ctx context.Context, id domain.SessionID, f ports.SCMFacts) error {
	tr, err := m.mutate(ctx, id, func(cur domain.CanonicalSessionLifecycle, exists bool) (domain.CanonicalSessionLifecycle, bool, error) {
		if !exists || !f.Fetched {
			return cur, false, nil
		}

		switch f.PRState {
		case domain.PRDraft, domain.PROpen:
			d := decide.ResolveOpenPRDecision(openPRInput(f))
			next := cur
			changed := setPRIfChanged(&next, d, f)
			return next, changed, nil

		case domain.PRMerged, domain.PRClosed:
			d := decide.ResolveTerminalPRStateDecision(f.PRState)
			next := cur
			changed := setPRIfChanged(&next, d, f)
			// A merge/close is a milestone that ends the work, so it parks the
			// session axis (idle / merged_waiting_decision) even over an
			// activity-owned needs_input/blocked — unlike the open-PR path,
			// which leaves the session axis to activity. A terminal session is
			// still never reopened.
			if !isTerminal(cur.Session.State) {
				changed = setSessionIfChanged(&next, d.SessionState, d.SessionReason) || changed
			}
			return next, changed, nil

		default: // none / unset: no PR-driven transition in split A
			return cur, false, nil
		}
	})
	if err != nil {
		return err
	}
	return m.react(ctx, id, tr, reactionContext{ciFailureLogTail: f.CIFailureLogTail})
}

// ApplyActivitySignal updates the activity axis. Only a valid-confidence signal
// is authoritative (stale/unavailable/probe_failure != idleness). It refreshes
// the persisted activity sub-state (the probe decider's RecentActivity input)
// and maps the classification onto the session axis. A valid signal is proof of
// life, so it may resolve a detecting session — clearing the quarantine memory
// so a later probe doesn't resume counting from a stale prior.
func (m *Manager) ApplyActivitySignal(ctx context.Context, id domain.SessionID, s ports.ActivitySignal) error {
	tr, err := m.mutate(ctx, id, func(cur domain.CanonicalSessionLifecycle, exists bool) (domain.CanonicalSessionLifecycle, bool, error) {
		if !exists || s.State != ports.SignalValid {
			return cur, false, nil
		}

		next := cur
		changed := false

		act := domain.ActivitySubstate{State: s.Activity, LastActivityAt: nowOr(s.Timestamp), Source: s.Source}
		if !sameActivity(cur.Activity, act) {
			next.Activity = act
			changed = true
		}
		if st, rs, ok := activityToSession(s.Activity); ok && shouldWriteSessionActivity(cur) {
			changed = setSessionIfChanged(&next, st, rs) || changed
			// Proof of life that pulls the session out of detecting must also
			// drop the quarantine memory (detecting memory only exists while
			// detecting, so this is a no-op otherwise).
			if cur.Detecting != nil {
				next.Detecting = nil
				changed = true
			}
		}

		return next, changed, nil
	})
	if err != nil {
		return err
	}
	return m.react(ctx, id, tr, reactionContext{})
}

// ---- mutation commands/outcomes reported by the Session Manager ----

// OnSpawnInitiated seeds or reopens the full session record for a spawn-like
// mutation. It is the Session Manager's create/reopen command under the Writer
// contract: the SM builds the identity + initial canonical row, but only the LCM
// writes it.
func (m *Manager) OnSpawnInitiated(ctx context.Context, rec domain.SessionRecord) error {
	return m.withLock(rec.ID, func() error {
		cur := rec.Lifecycle
		if current, ok, err := m.store.Get(ctx, rec.ID); err != nil {
			return err
		} else if ok {
			currentLC := current.Lifecycle
			if !isTerminal(currentLC.Session.State) && !isTerminal(rec.Lifecycle.Session.State) {
				return fmt.Errorf("lifecycle: OnSpawnInitiated for active session %q", rec.ID)
			}
			cur = currentLC
		}
		rec.Lifecycle = m.prepareLifecycleWrite(cur, rec.Lifecycle)
		now := m.clock()
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = now
		}
		rec.UpdatedAt = now
		return m.store.Upsert(ctx, rec)
	})
}

// OnSpawnCompleted records that a spawn finished: the runtime is up and the
// handles are known. Per the agreed rule it flips the runtime axis to alive and
// stores the handles in metadata, but leaves the session at not_started
// (display: spawning) — the agent "acknowledges" via the first activity signal.
func (m *Manager) OnSpawnCompleted(ctx context.Context, id domain.SessionID, o ports.SpawnOutcome) error {
	return m.withLock(id, func() error {
		rec, exists, err := m.store.Get(ctx, id)
		if err != nil {
			return err
		}
		if !exists {
			// The SM seeds the initial lifecycle before spawning; a completion
			// for an unseeded session is a contract violation, not a stray
			// observation, so surface it rather than fabricating a record.
			return fmt.Errorf("lifecycle: OnSpawnCompleted for unseeded session %q", id)
		}
		rt := domain.RuntimeSubstate{State: domain.RuntimeAlive, Reason: domain.RuntimeReasonProcessRunning}
		if rec.Lifecycle.Runtime != rt {
			cur := rec.Lifecycle
			next := cur
			next.Runtime = rt
			rec.Lifecycle = m.prepareLifecycleWrite(cur, next)
			rec.UpdatedAt = m.clock()
			if err := m.store.Upsert(ctx, rec); err != nil {
				return err
			}
		}
		if meta := spawnMetadata(o); len(meta) > 0 {
			if err := m.store.PatchMetadata(ctx, id, meta); err != nil {
				return err
			}
		}
		return nil
	})
}

// OnKillRequested is the SM's explicit terminal-write authority (the one
// terminal path that does not go through the inferred-death decider). It writes
// the terminal session/runtime sub-states for the kill kind and clears any
// in-flight detecting memory.
func (m *Manager) OnKillRequested(ctx context.Context, id domain.SessionID, r ports.KillReason) error {
	// An explicit user kill is a human action, not an inferred event, so it
	// fires no reaction — the transition is discarded.
	_, err := m.mutate(ctx, id, func(cur domain.CanonicalSessionLifecycle, exists bool) (domain.CanonicalSessionLifecycle, bool, error) {
		if !exists {
			// Killing an unknown/already-gone session is a benign race; no-op
			// rather than fabricating a terminal record for a session we never
			// knew about.
			return cur, false, nil
		}

		next := cur
		changed := false

		if sess := killSession(r.Kind); cur.Session != sess {
			next.Session = sess
			changed = true
		}
		if rt := killRuntime(r.Kind); cur.Runtime != rt {
			next.Runtime = rt
			changed = true
		}
		if cur.Detecting != nil {
			next.Detecting = nil
			changed = true
		}
		return next, changed, nil
	})
	if err != nil {
		return err
	}
	// A kill is terminal but bypasses react()'s incident-over cleanup (it fires
	// no reaction). Drop any escalation trackers here so a later duration-based
	// TickEscalations can't emit reaction.escalated for a dead session.
	m.clearSessionTrackers(id)
	return nil
}

// ---- diff helpers ----

// setSessionIfChanged sets next.Session only when the decided sub-state differs
// from the current next value; an empty decided state means "decider does not
// address the session axis" and is left untouched.
func setSessionIfChanged(next *domain.CanonicalSessionLifecycle, st domain.SessionState, rs domain.SessionReason) bool {
	if st == "" {
		return false
	}
	want := domain.SessionSubstate{State: st, Reason: rs}
	if next.Session == want {
		return false
	}
	next.Session = want
	return true
}

// setPRIfChanged folds the decided PR sub-state plus the fact-borne PR identity
// (number/url) into next when it differs from the current next value.
func setPRIfChanged(next *domain.CanonicalSessionLifecycle, d decide.LifecycleDecision, f ports.SCMFacts) bool {
	want := domain.PRSubstate{State: d.PRState, Reason: d.PRReason, Number: f.PRNumber, URL: f.PRURL}
	if next.PR == want {
		return false
	}
	next.PR = want
	return true
}

// setDetecting implements the detecting semantics on the full canonical row:
// set/replace when the decision carries memory, clear (#3) when it doesn't but
// canonical still holds stale memory, else leave untouched.
func setDetecting(next *domain.CanonicalSessionLifecycle, d *domain.DetectingState) bool {
	if d != nil {
		if next.Detecting != nil && *next.Detecting == *d {
			return false
		}
		dc := *d
		next.Detecting = &dc
		return true
	}
	if next.Detecting != nil {
		next.Detecting = nil
		return true
	}
	return false
}

// sameActivity compares activity sub-states with time-aware equality (== on
// time.Time is monotonic-clock sensitive and would spuriously report changes).
func sameActivity(a, b domain.ActivitySubstate) bool {
	return a.State == b.State && a.Source == b.Source && a.LastActivityAt.Equal(b.LastActivityAt)
}

func spawnMetadata(o ports.SpawnOutcome) map[string]string {
	meta := map[string]string{}
	if o.Branch != "" {
		meta[MetaBranch] = o.Branch
	}
	if o.WorkspacePath != "" {
		meta[MetaWorkspacePath] = o.WorkspacePath
	}
	if o.RuntimeHandle.ID != "" {
		meta[MetaRuntimeHandleID] = o.RuntimeHandle.ID
	}
	if o.RuntimeHandle.RuntimeName != "" {
		meta[MetaRuntimeName] = o.RuntimeHandle.RuntimeName
	}
	if o.AgentSessionID != "" {
		meta[MetaAgentSessionID] = o.AgentSessionID
	}
	return meta
}
