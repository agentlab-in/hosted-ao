# CLAUDE.md

Read and follow [`AGENTS.md`](AGENTS.md) for repository layout, commands, coding conventions, and hard rules.

## AgentLab releases are unsigned

Publish only unsigned builds from `agentlab-in/hosted-ao`. Do not configure or
use code-signing or notarization credentials, and do not describe AgentLab
artifacts as signed or notarized. Preserve checksums, provenance, updater feeds,
and artifact-completeness verification. See the distribution rules in
`AGENTS.md` and the reusable intake procedure in
`docs/upstream-merge-playbook.md`.

## App state lives under `~/.ao/hosted` only

All app state, the daemon's data dir, `running.json`, worktrees, repos,
`machine.json`, and the Electron supervisor's `userData` (Chromium cache, cookies,
local/session storage, crash dumps), must resolve under `~/.ao/hosted`
(overridable via `AO_DATA_DIR`/`AO_RUN_FILE`). Never write to or read from
`~/Library/Application Support` or any other OS-default app-data location.
`frontend/src/main.ts` pins Electron's `userData` to `~/.ao/hosted/electron`; do
not remove that override.

The subdirectory is not decoration. The upstream agent-orchestrator app writes its
own `running.json` and `ao.db` directly under `~/.ao`, so a bare `~/.ao` default
makes the two builds fight over daemon discovery, the pid, the port, and a SQLite
file whose goose migration history the other build owns. Derive the default from
`config.StateRootSegments()` (Go) or `frontend/src/shared/state-root.ts`
(main/renderer); never re-spell the path. See the hard rule in `AGENTS.md`.

## Design System

Always read [`DESIGN.md`](DESIGN.md) before making any visual or UI decision —
**start with the "clone agent-orchestrator verbatim" banner at the top**, which
governs the current look.

The renderer **clones the agent-orchestrator web app verbatim**
(`~/Projects/agent-orchestrator/packages/web/src`) in looks and design, with a
refined-blue accent and the terminal keeping its own palette. This **supersedes the
older design-reference framing** in DESIGN.md (per explicit user decision 2026-06-10).
Build new UI from shadcn primitives (`components/ui/*`) where a component fits. Do not
deviate without explicit user approval. In QA/review, flag any renderer code that
diverges from **agent-orchestrator** — do **not** re-flag old design-reference mismatches.

When showing or demoing frontend changes, run `ao preview [url]` from inside the
session so the change renders in the desktop browser panel (the inspector rail's
Browser tab); do not just describe it. `ao preview` updates the panel non-disruptively —
it does not steal focus or force the Browser tab open if the user is looking at
something else, only badging it as unseen. If the Browser tab isn't already the one
the user has open, say so in your reply (e.g. "check the Browser tab") so the change
doesn't go unnoticed behind the badge.
