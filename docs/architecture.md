# LCM + Session Manager — architecture

This is the deterministic core of the backend daemon. It supervises agent
sessions and keeps exactly one true status per session.

## 1. Mental model: OBSERVE → DECIDE → ACT

The backend owns no agent state. git/GitHub own PR/CI/review truth; the agent's
own files own its activity. The job, per session, is one loop:

```
OBSERVE            →   DECIDE              →   ACT
(impure, external)     (pure, total)           (impure)
raw facts              one canonical status    persist + react
```

In the rewrite the **OBSERVE** step lives *outside* the LCM (separate owners),
and the LCM is a **synchronous reducer** invoked with facts:

```
SCM poller     ─ ApplySCMObservation ──┐
reaper         ─ ApplyRuntimeObservation┤
activity hooks ─ ApplyActivitySignal ───┼─▶ LCM:  load canonical
Session Mgr    ─ OnSpawnCompleted ──────┘         → pure DECIDE
               ─ OnKillRequested                  → diff → persist (merge-patch)
reaper tick    ─ TickEscalations                  → if transition: react (ACT)
```

The LCM **never polls**. The reaper (a timer, owned elsewhere) drives liveness
sampling and duration-based escalation by calling in.

## 2. Canonical state model — the crown jewel

The **only** thing persisted per session is `CanonicalSessionLifecycle`
(`backend/internal/domain/lifecycle.go`). The single-word display status is
**derived on read and never stored** — this is the most important invariant; it
prevents canonical truth and display from drifting.

```
CanonicalSessionLifecycle
  Version    schema version of the record shape
  Revision   monotonic write counter (optimistic-concurrency token)
  Session    (state, reason)   working/idle/needs_input/stuck/detecting/done/terminated
  PR         (state, reason)   none/open/merged/closed
  Runtime    (state, reason)   unknown/alive/exited/missing/probe_failed
  Activity   last-known agent activity (+ timestamp, source)   ← decider input
  Detecting  anti-flap quarantine memory (nil unless quarantined) ← decider input
```

`DeriveLegacyStatus` (`domain/status.go`) is the **sole producer** of the
display `SessionStatus`. Precedence: terminal/hard session states map directly
(they outrank PR facts) → a merged PR wins → an open PR maps by reason → else the
soft session state. So an idle worker with a CI-failing open PR displays
`ci_failed`, but a `needs_input` session shows `needs_input` regardless of the PR.

`Session` (`domain/session.go`) is the read-model: a `SessionRecord`
(persistence shape, identity + lifecycle + metadata) plus the derived `Status`.
The **Session Manager is the single producer of `Status`** — it attaches it on
read; the store and API never recompute or persist it.

## 3. Package layout (`backend/internal/`)

```
domain/                 the vocabulary (imports only the std lib → no cycles)
  lifecycle.go          CanonicalSessionLifecycle + all sub-states/enums
  status.go             SessionStatus + DeriveLegacyStatus (sole display producer)
  session.go            SessionRecord (persisted) + Session (read-model) + id types
  decide/               the PURE core — total, deterministic, zero I/O
    types.go            LifecycleDecision + Probe/OpenPR/Detecting inputs + tuning consts
    decide.go           the deciders + the anti-flap quarantine + HashEvidence
ports/                  the boundaries (interfaces + DTOs)
  inbound.go            LifecycleManager, SessionManager (we implement)
  outbound.go           LifecycleStore, Notifier, AgentMessenger, Runtime/Agent/Workspace
  facts.go              SCMFacts, RuntimeFacts, ActivitySignal, SpawnOutcome, KillReason
lifecycle/              the LCM implementation (DECIDE + ACT)
  manager.go            the Apply* pipeline, per-session lock, patch diffing
  decide_bridge.go      fact→decide-input translation + the composition rules
  reactions.go          the reaction table + escalation engine + TickEscalations
session/                the SM implementation (explicit mutations)
  manager.go            Spawn/Kill/Restore/Cleanup/List/Get/Send + rollback
```

`domain` + `ports` are the committed, stabilized **integration boundary**.
Everything else implements behind it.

## 4. The pure DECIDE core (`domain/decide`)

Total, deterministic, side-effect-free functions — the highest-value test
surface (table-tested to 100%). Key ones:

- `ResolveProbeDecision` — runtime/process liveness. An explicit kill
  short-circuits to terminal; a **failed probe is never read as death** (routes
  to `detecting`), as does any probe disagreement; only runtime-dead +
  process-dead + no-recent-activity reaches `killed`.
- `ResolveOpenPRDecision` — the PR ladder: `ci_failing` → `changes_requested` →
  `mergeable` → `approved` → `review_pending` → idle-beyond → else `pr_open`.
- `ResolveTerminalPRStateDecision` — merged → `merged` (park idle awaiting a
  human decision); closed → `idle`.
- `CreateDetectingDecision` — the **anti-flap quarantine**. Counts attempts and
  hashes the *timestamp-stripped* evidence; escalates to `stuck` only after 3
  consecutive unchanged-evidence ticks **or** 5 minutes since first entering
  detecting (`StartedAt` is preserved across the whole episode). Changing
  evidence resets the counter.

## 5. The LCM (`lifecycle`)

Implements `ports.LifecycleManager`. Every `Apply*`/`On*` entrypoint runs the
same pipeline (`manager.go`):

```
withLock(session):                       ← per-session serialization
  load canonical → decideFn (build sparse patch) → if changed: persist → load after
return transition (before, after)
```
then, **after the lock releases**, `react()` fires the mapped reaction.

- **Per-session serialization** — `keyedMutex` hands out one lock per session id
  (parallel across sessions, serial within one). Entries are reference-counted
  and evicted when the last holder releases, so the map stays bounded.
- **Composition rules** (`decide_bridge.go`) — two observers must not fight over
  the session axis. Liveness (runtime probes) owns the runtime + death/detecting
  axis; activity owns working/idle/waiting. `isLivenessOwned` decides when a
  healthy probe may *recover* a state (e.g. `detecting → working`) vs. when it
  must not clobber an activity-owned `needs_input`/`blocked`. A high-confidence
  activity signal may resolve a `detecting` session; an open PR writes only the
  PR axis and lets `DeriveLegacyStatus` surface it.
- **Detecting-memory lifecycle** — a decision with `Detecting == nil` clears the
  persisted quarantine memory (`LifecyclePatch.ClearDetecting`) so a stale prior
  can't leak into a later episode.
- **ACT — reactions + escalation** (`reactions.go`) — on a genuine status
  transition, `react()` maps it to a reaction (`send-to-agent` / `notify`;
  `auto-merge` exists but is off by default) and dispatches it. A
  per-`(session,reaction)` escalation tracker counts attempts; it escalates
  (notifies a human and silences further auto-dispatch) when a numeric cap or a
  duration is exceeded. The `ci-failed` budget is persistent across CI
  oscillation within an open PR and re-arms on genuine recovery. `TickEscalations`
  (called by the reaper) fires the duration-based escalations the synchronous
  LCM can't wake itself for; it notifies outside the lock.

## 6. The Session Manager (`session`)

Implements `ports.SessionManager` — the explicit-mutation plumbing. It never
derives/observes lifecycle state; it routes outcomes to the LCM.

- **Spawn** — `Workspace.Create` → build prompt → `Runtime.Create` (env
  `AO_SESSION_ID`/`AO_PROJECT_ID`/`AO_ISSUE_ID`) → **seed** the initial record
  (`not_started`/`spawn_requested`) via the store → `LCM.OnSpawnCompleted`.
  Eager rollback unwinds prior steps on failure; an `OnSpawnCompleted` failure
  routes the seeded orphan to terminal-errored (the store has no delete; a later
  `Cleanup` reclaims it).
- **Kill** — `LCM.OnKillRequested` → `Runtime.Destroy` → `Workspace.Destroy`,
  honoring the **worktree-remove safety**: after `git worktree prune`, a still-
  registered path is never `rm -rf`'d (it may hold the agent's uncommitted work)
  — the refusal is surfaced, not forced.
- **Restore** — reopen via `PatchLifecycle` (not re-seed): session →
  `not_started`, PR → `cleared_on_restore`; relaunch with the agent's resume
  command; runtime is rolled back on a post-create failure.
- **List/Get** — read records and attach the derived `Status`. **Send** — via
  `AgentMessenger`. **Cleanup** — tear down terminal/stale sessions, skipping
  paths with uncommitted work.

## 7. Load-bearing invariants

1. **Persist canonical; derive display.** Never store the display status.
2. **One authority for death.** Only the DECIDE pipeline (via `detecting`) writes
   inferred terminal states; the SM's explicit-kill path goes through
   `OnKillRequested`. Everything else that notices a dead runtime persists
   `detecting`, never `terminated`.
3. **Failed probe ≠ dead.** Timed-out/errored probes route to `detecting`.
4. **Evidence-hash debounce** prevents flapping signals from terminating live
   work; the 5-minute cap is a whole-episode wall-clock safety net.
5. **PR facts dominate** the soft session states once a PR exists.
6. **Merge-patch persistence** — writes touch only changed keys; the store is the
   single disk writer (atomic write + lock + CDC).
7. **Sticky activity states** (`waiting_input`/`blocked`) do not decay by clock.
8. **Worktree-remove safety** on teardown.

## 8. Concurrency & testing

- Within a session, the per-session lock serializes the load→decide→persist
  read-modify-write. `react()` runs *outside* the lock (so a busy-waiting
  send-to-agent never holds the session mutex) — see `status.md` for the
  integration-time follow-up this implies.
- Tests use **in-memory fakes** for every outbound port, so the LCM and SM are
  fully testable with no real adapters. The SM tests drive the **real**
  `lifecycle.Manager` for spawn/kill round-trips, so the SM↔LCM contract is
  genuinely exercised. The `decide` package is table-tested in isolation.
