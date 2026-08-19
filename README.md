<div align="center">
  <img src="assets/ao-logo.svg" alt="Hosted AO" width="160" height="160" />

# Hosted AO

**Agent Orchestrator, with a cloud under it.**

[![Upstream](https://img.shields.io/badge/upstream-Untrivial--ai%2Fagent--orchestrator-blue)](https://github.com/Untrivial-ai/agent-orchestrator)
[![Control plane](https://img.shields.io/badge/control%20plane-ao.agentlab.in-0b7285)](https://ao.agentlab.in)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

One desktop app that runs parallel AI coding agents on your machine and on your own cloud VMs, side by side, behind one account.

<img src="docs/assets/readme/hero.png" alt="Hosted AO board showing parallel coding agent sessions" width="100%" />
</div>

## What this is

[Agent Orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) is a local desktop workspace for agent-driven development: give every coding task its own agent, branch, and worktree, plan larger outcomes with a project-aware orchestrator, and follow every worker, pull request, CI run, and review on one live Kanban.

Hosted AO keeps all of that and adds the part upstream deliberately does not have: **machines that are not yours to babysit**.

Sign in once, or pair a box in one command. Your board now shows local sessions and remote sessions together, and an agent running on a VM in another country attaches its terminal into your desktop as if it were local.

## What you get from upstream

Everything upstream ships, this ships. The highlights:

- **Workers.** One task, one coding agent, one isolated workspace. Git-backed workers get their own branch and worktree; Scratch workers get AO-managed branchless directories.
- **A project orchestrator.** A persistent planning agent that reasons about direction and sequence, then breaks a plan into focused tasks and spawns or redirects workers to carry them.
- **A live Kanban.** Cards derive their position from session, pull request, CI, and review facts: Working, Needs you, In review, Ready to merge.
- **Pull requests and agent reviews.** CI, mergeability, reviewer state, and interactive agent reviews beside the worker, with requested changes returned to the same owner.
- **An agent-controllable browser.** Preview and inspect a worker's local app beside its interface, with browser profiles isolated per worker.
- **26 coding agents**, including Claude Code, Codex, Cursor, opencode, Aider, GitHub Copilot, Amp, Droid, Cline, Goose, Kiro, Muse, and more, through one supervised workflow. [Browse agent setup guides](https://aoagents.dev/docs/plugins/agents).
- **Native interfaces, one supervisor.** Structured Chat or the agent's own terminal UI, with task context, workspace state, and feedback kept in one place.

## How the hosting layer fits together

Three pieces, each with one job:

| Piece | Job |
|---|---|
| **Hosted AO desktop** (this repo) | The Electron app. Runs the local daemon, signs in to the control plane or holds a pinned pairing, and speaks an authenticated transport (REST, SSE, terminal WebSocket) to whichever machine you point it at. |
| **VM gateway** (`ao vm serve`, this repo) | The only public-facing process on a machine. TLS, per-machine credential verification, and a deny-by-default reverse proxy in front of a loopback-only daemon. |
| **Control plane** (private, [ao.agentlab.in](https://ao.agentlab.in)) | Accounts, the machine registry, and short-lived token minting. Optional, and never in the data path: your code and your terminals flow desktop-to-machine directly. |

The daemon itself never grows a public listener. That is a hard rule inherited from upstream and enforced harder here: the gateway is a separate process, the loopback daemon stays unauthenticated on 127.0.0.1, and the gateway's path allowlist decides what the outside world may ever ask.

## What the hosting layer adds

- **Pairing, no account required.** Pair a box you can already reach using a self-signed certificate, a pinned SHA-256 fingerprint, and an 8-character passcode. No DNS, no domain, no sign-up.
- **One account, many machines.** Sign-in, a machine picker in the app, and unbindable machine registrations on the account page.
- **One-command VM bootstrap.** `ao setup-vm` takes a fresh Ubuntu box to a registered, TLS-serving agent machine: dependency preflight, systemd units for daemon and gateway, device-flow binding, done.
- **Real remote sessions.** Bearer-authenticated REST and SSE, cookie-authenticated terminal mux and event streams, silent token refresh. The board, the terminal, notifications, all of it works against a remote machine.
- **Clone by URL, on the machine that runs the agents.** Add a project from a Git URL and the
  active machine clones it into its own managed repos directory; nothing needs to pre-exist on it,
  and no folder on your desktop is involved. Local machines keep the ordinary "choose where to
  clone" flow.
- **Remote health.** `GET /api/v1/doctor` serves the same checks `ao doctor` runs, through the gateway, so the app can tell you a machine's harness auth is broken before you waste a session on it.
- **Perfect coexistence.** All state lives under `~/.ao/hosted`. Install Hosted AO next to a stock Agent Orchestrator and neither knows the other exists.

## Getting started

```bash
# Desktop (macOS)
npm install
cd frontend && npm install && npm run make
open "out/make/Hosted AO-"*.dmg

# A VM (Ubuntu, with a DNS name pointed at it)
ao setup-vm
```

Sign in or pair from the app, pick your machine, spawn agents.

## Documentation

| Document | Start here when you need |
|---|---|
| [docs/architecture.md](docs/architecture.md) | Backend mental model, lifecycle, persistence, CDC, status derivation, and daemon boundaries. |
| [docs/backend-code-structure.md](docs/backend-code-structure.md) | Package ownership and where each backend concern belongs. |
| [docs/cli/README.md](docs/cli/README.md) | CLI behavior and daemon route mapping. |
| [docs/development.md](docs/development.md) | Prerequisites, build steps, running tests, and troubleshooting for local development. |
| [docs/STATUS.md](docs/STATUS.md) | What currently ships and what remains in flight. |
| [docs/upstream-merge-playbook.md](docs/upstream-merge-playbook.md) | How to merge upstream into this repo without losing the hosting layer. |
| [Upstream product docs](https://aoagents.dev/docs) | Installation, agent setup, and day-to-day product usage. |

## Relationship to upstream

This repo is a true fork of [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) and tracks it continuously: upstream's product is merged wholesale, and the hosting layer stays deliberately thin around it. Everything upstream ships, this ships.

## Anonymous telemetry

AO uses privacy-preserving product usage and reliability metrics designed to exclude PII and project content. [Learn more about telemetry and privacy](docs/telemetry.md).

## License

Licensed under the [Apache License 2.0](LICENSE), same as upstream.
