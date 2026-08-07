# agent-orchestrator status

Current `main` ships a working single-user local loop: the Go daemon and the
Electron/React frontend both drive a live daemon over HTTP/SSE/WebSocket. The
core GitHub flow works end-to-end: add project → spawn session/orchestrator →
attach terminal → observe PR → merge.

This file tracks progress. For what the product _is_ and how to run it, see the
top-level [`README.md`](../README.md); for the backend mental model see
[`architecture.md`](architecture.md).

## Build & test

The local gate is the backend Go build and race-enabled test suite:

```bash
cd backend && go build ./... && go test -race ./...
```

`npm run lint` (from the repo root) runs `go test ./...` plus golangci-lint.
Frontend checks live under `frontend/` (`npm run typecheck`, `npm run build`).
See [`AGENTS.md`](../AGENTS.md) for the regen workflow when touching the API
surface (`npm run sqlc`, `npm run api`).

## Shipped

### Backend (Go daemon)

- Loopback-only HTTP daemon (chi router, CORS, per-request timeout,
  `/healthz` / `/readyz` / `/shutdown`).
- SQLite store with goose migrations and sqlc-generated queries; DB
  trigger-based change-data-capture into `change_log`.
- CDC poller + broadcaster feeding in-process subscribers and the SSE stream
  at `GET /api/v1/events` (with `Last-Event-ID` replay).
- Full session lifecycle over HTTP: list, get, spawn, kill, restore, rename,
  rollback, cleanup, send, activity, PR claim/list. Orchestrator routes
  (list/spawn/get) are wired too.
- One daemon-committed interface per session. TUI sessions retain the established
  tmux/conpty agent runtime; Chat sessions use runtime-less native controllers,
  persist provider conversation identity, and dispatch lifecycle reactions
  through the same mode-aware session manager. A durable, capability-gated
  drain/interrupt handoff can move the same Claude Code or Codex native
  conversation between TUI and Chat without changing the AO session/worktree;
  rollback, restart recovery, controller-generation fencing, and a transition
  message outbox preserve the one-controller invariant.
- Durable Chat conversations with project-scoped orchestrator continuity,
  session-scoped worker history, bounded history pages, transactional raw-event
  archive/projection, controller-generation fencing, turns, messages,
  activities, approvals, structured input, usage, compaction, and rollback.
- Chat drivers for the user's installed Codex (native app-server), Claude Code
  (claude-agent-acp), OpenCode, and Droid. AO reuses each harness's existing
  binary/auth resolution and does not bundle provider CLIs.
- Project CRUD plus per-project config (`PUT /projects/{id}/config`).
- PR action engine wired into the API: `POST /prs/{id}/merge` and
  `/prs/{id}/resolve-comments`.
- Review routes registered: `GET /reviews`, `POST /reviews/execute`,
  `POST /reviews/{id}/send`.
- Durable dashboard notifications for `needs_input`, `ready_to_merge`,
  `pr_merged`, and `pr_closed_unmerged`: backend enrichment/persistence,
  cursor-paginated read/unread history, live notification stream, and read
  acknowledgement API.
- SCM observer (`internal/observe/scm`) wired into the daemon: GitHub provider,
  lazy/non-blocking auth, per-PR polling with ETag guards and semantic diffing,
  feeding PR facts into lifecycle, which sends agent nudges for CI failures,
  review feedback, and merge conflicts
  ([#75](https://github.com/aoagents/agent-orchestrator/issues/75),
  [#108](https://github.com/aoagents/agent-orchestrator/issues/108),
  [#109](https://github.com/aoagents/agent-orchestrator/issues/109)).
- Terminal mux over WebSocket (`/mux`): per-client `tmux attach` PTY on
  Darwin/Linux; conpty loopback pty-host on Windows.
- Lifecycle reducer plus reaper (`internal/observe/reaper`).
- Agent adapter platform under `internal/adapters/agent/` (24 adapters) with a
  registry and `ao hooks` activity dispatch.
- OpenAPI spec generated from Go DTOs; frontend TS types generated from it and
  drift-checked in CI.

### Frontend (Electron + React)

- Electron + React 19 + TanStack Router/Query + Tailwind + shadcn primitives.
- Target-isolated per-session browser-control spike: a dedicated local
  daemon↔Electron bridge drives only the selected session's `WebContentsView`
  through Electron's bound debugger transport. `ao browser` supports open,
  compact accessibility snapshots and refs, click/fill/type, keyboard input,
  hover and non-mutating element highlighting, scrolling, selection and checked
  state, property reads, stable logical tabs and captured popups, a compact
  user-facing tab selector for switching/closing tabs and popup notices, waits,
  including load/disappearance/DOM-stability conditions, screenshots, console
  messages, page errors, and explicit temporary network-metadata capture while
  the Browser panel is hidden. Network capture is off by default, tab-scoped,
  bounded, automatically expires, and omits bodies and sensitive values. Tabs
  within one worker share an ephemeral Electron profile; different workers
  have isolated cookies and web storage. The toolbar activity signal is scoped
  to actual agent browser commands; annotation progress is separate and its
  successful-delivery confirmation clears automatically.
- Preview targets are explicit: `ao preview`, `ao preview <target>`, or
  `ao preview start` selects what the panel shows. The desktop poller no longer
  auto-discovers a static entry point merely because a fresh worker exists.
- Real daemon wiring via the generated `openapi-fetch` typed client
  (`src/api/schema.ts`); mock data only in `VITE_NO_ELECTRON` web-preview mode.
- Electron main handles daemon discovery, launch, and status reporting.
- Shell: sidebar (projects + sessions, add/remove project), sessions board,
  session view + inspector, project settings, pull-requests page,
  spawn-orchestrator flow.
- SessionView renders from the session's persisted mode: the existing terminal
  surface for TUI, or the durable Chat timeline/composer for Chat. Chat retains
  access to session-scoped worktree shells without creating an agent tmux pane.
- Compatible Claude Code and Codex sessions expose an in-session “Open Chat” /
  “Open Terminal UI” action. Idle sessions switch directly; busy sessions offer
  an explicit finish-and-drain or stop-and-interrupt policy and show durable
  progress/recovery state.
- Desktop status and SCM summary V1: session status comes from
  `GET /api/v1/sessions`; visible/active PR context comes from
  `GET /api/v1/sessions/{sessionId}/pr`; `GET /api/v1/events` is kept open as
  an invalidation stream rather than a full PR payload stream.
- Concise PR summaries include PR identity, CI state with failing check names
  and links, human reviewer IDs/counts/links for unresolved review comments,
  and mergeability reasons. Raw CI logs and review comment bodies are
  intentionally not part of the desktop V1 API/UI.
- Terminal pane (xterm) over the mux WebSocket, with a live SSE events
  connection and port-rebind on daemon restart.
- Chat history uses bounded pages and targeted CDC/SSE invalidation rather than
  polling and transferring the full lifetime of a conversation.
- In-app notification center with click access, Unread/All filters, paginated
  REST catch-up, live notification stream updates, separate PR/session target
  actions, persistent read history, mark-read controls, and Electron app toasts
  while the app is running.

### Mobile (Expo + React Native)

- Connect Mobile pairs with the daemon's opt-in authenticated LAN listener; the
  loopback listener and its security model remain unchanged.
- New mobile workers and orchestrators request Chat mode by default. Worker
  creation filters to the daemon-advertised Chat harnesses, while Terminal UI
  remains an explicit compatibility choice and typed Chat preflight failures
  offer that fallback.
- Session routing uses the same daemon-committed mode as desktop. TUI keeps
  the existing authenticated mux/xterm surface; Chat uses the same durable,
  paged conversation projection and CDC/SSE invalidation stream as desktop.
- Mobile exposes the same capability-gated TUI↔Chat handoff, busy-turn policy,
  cancellation window, progress overlay, and automatic renderer swap after the
  daemon commits the new controller.
- Native Chat includes prose/Markdown, provider activity, commands, plans,
  changed files, approvals, structured input, model/effort/provider controls,
  compaction, rollback, MCP recovery, skills and file references, staged/native
  image delivery, embedded text resources, voice dictation, retryable delivery,
  persisted drafts, and a session-scoped worktree shell through the existing
  terminal mux.

## Hosted AO v1: accounts and registered machines

Lives on `develop` (not yet the story on `main`). Spec:
[`superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md`](superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md).
Decisions, the PR-per-task table, and the review history:
[`hosted-ao-v1-build-log.md`](hosted-ao-v1-build-log.md).
Real-VM verification writeup: [`../hosted-log.md`](../hosted-log.md)
([#68](https://github.com/agentlab-in/hosted-ao/pull/68)).

**Tasks 1 through 14 are done.** Batches 1 through 4 plus batch 5 transport and the
fresh-VM run are on `develop`. Mid-build additions that stayed:
`GET /api/v1/doctor` ([#36](https://github.com/agentlab-in/hosted-ao/pull/36)),
`POST /api/v1/machines/{id}/token` ([#50](https://github.com/agentlab-in/hosted-ao/pull/50)),
desktop OAuth authorize/token ([#62](https://github.com/agentlab-in/hosted-ao/pull/62)),
reachability probe and post-login landing ([#61](https://github.com/agentlab-in/hosted-ao/pull/61)).
Verification fixes that closed the two e2e blockers:
gateway honors `AO_ALLOWED_ORIGINS` ([#66](https://github.com/agentlab-in/hosted-ao/pull/66)),
remote project create accepts `status.baseUrl`
([#67](https://github.com/agentlab-in/hosted-ao/pull/67)).

### Shipped on `develop`

- **Control plane** (`controlplane/`, live at `https://ao.agentlab.in`): Google login with
  PKCE, desktop OAuth authorize/token, EdDSA signing keys and JWKS, access and refresh
  token issuance with rotation, the RFC 8628 device flow, the machine registry,
  authenticated `GET /api/v1/machines`, machine-audience token mint at
  `POST /api/v1/machines/{id}/token`, off-box reachability probe, post-login landing page.
- **VM gateway** (`ao vm serve`): ACME TLS, JWT verification against the control plane's
  JWKS, machine allowlist, reverse proxy to the loopback daemon, deny-by-default path
  allowlist, CORS preflight handling (including `AO_ALLOWED_ORIGINS`).
- **CLI**: `ao setup-vm` (Ubuntu preflight, dependency install, systemd units for daemon and
  gateway, device binding, `machine.json`), `ao vm setup-harness claude`, `ao whoami`, and
  `cloneUrl` on `POST /api/v1/projects`.
- **Desktop**: AO account login through the system browser, machine list / picker /
  switching, authenticated remote transport (Bearer on REST and SSE, JWT-valued
  `ao_gw_token` cookie for `/mux`, silent refresh), remote project create by Git URL.
- **State isolation**: hosted desktop and daemon state default under `~/.ao/hosted`
  ([#60](https://github.com/agentlab-in/hosted-ao/pull/60)) so they never fight the
  upstream agent-orchestrator install over `~/.ao`.

### Verified end to end (2026-07-31)

On a real user-owned VM (`vm-test.ao.agentlab.in`), from a signed-out desktop:

1. Google sign-in against `https://ao.agentlab.in`
2. Account machines listed; remote machine selected
3. Project cloned by Git URL onto the VM over the machine-audience JWT path
4. Claude harness auth on the VM reported `PASS` via `ao doctor`

Details, operator redeploy notes, and snapshot restore behavior:
[`../hosted-log.md`](../hosted-log.md).

### Task 15 and account home (done)

- **Env-var pairing path retired.** `AO_REMOTE_URL`, `AO_REMOTE_TOKEN`, and the
  `ao_hosted_pair` cookie are gone from the desktop. Remote is accounts only.
  `AO_CONTROL_URL` remains the development hatch (which control plane to trust; never
  skips authentication). `deploy/hosted/` documents the old Caddy pairing proxy as
  retired.
- **Account home on the control plane** (`GET /`): lists machines bound to the signed-in
  account, unbind via `POST /machines/unbind` (session + same-origin), and empty-state
  setup copy for `ao setup-vm`. API unbind: `DELETE /api/v1/machines/{id}`.

Three code-review reports covering batches 1 through 4 are preserved in
[`reviews/`](reviews/), with every finding mapped to the PR that fixed it.

## In flight / not yet a runtime feature

- **Cross-interface visual history import**: provider-native context continues
  across a compatible handoff, and Chat history already recorded by AO remains
  durable. A first TUI→Chat switch does not reconstruct terminal screen output
  as structured AO messages/tool cards; doing so requires a provider history
  import contract with stable identities and deduplication.
- **In-flight tool portability**: drain can finish accepted work and interrupt
  can cancel it, but no common provider protocol serializes a currently executing
  tool call or detached background process for adoption by another controller.

- **Tracker lane**: GitHub tracker adapter exists, but there is no daemon
  observer loop or agent-lifecycle→issue mirroring yet, so the tracker does
  nothing at runtime ([#112](https://github.com/aoagents/agent-orchestrator/issues/112)).
- **Full raw PR/tracker fact surfacing**: the SCM observer writes facts and the
  desktop consumes concise PR summaries, but exposing the full raw `pr_*` /
  `tracker_*` CDC events to live consumers
  ([#110](https://github.com/aoagents/agent-orchestrator/issues/110)) and in
  `ao session get` ([#111](https://github.com/aoagents/agent-orchestrator/issues/111))
  is still open.

Tracking milestone:
[`rewrite`](https://github.com/aoagents/agent-orchestrator/milestone/1).
