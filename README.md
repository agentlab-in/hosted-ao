<div align="center">
  <img src="assets/ao-logo.svg" alt="Hosted AO" width="160" height="160" />

# Hosted AO

**Run AI coding agents across your machines from one desktop workspace.**

[![Upstream](https://img.shields.io/badge/upstream-Untrivial--ai%2Fagent--orchestrator-blue)](https://github.com/Untrivial-ai/agent-orchestrator)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

Hosted AO combines the Agent Orchestrator desktop experience with direct, secure access to agent machines you control.

<img src="docs/assets/readme/hero.png" alt="Hosted AO board showing parallel coding agent sessions" width="100%" />
</div>

## What Hosted AO does

Hosted AO is a desktop supervisor for agent-driven development. Each task gets its own coding agent and isolated workspace, while the app keeps sessions, pull requests, CI, reviews, terminals, and previews together.

Run agents on your local machine or pair another machine you can reach, including a home server, spare computer, or cloud VM. Remote traffic goes directly between the desktop and that machine.

## Capabilities

- **Isolated workers.** Give each task its own agent, branch, and worktree. Scratch workers use AO-managed branchless directories when a Git workflow is unnecessary.
- **Project orchestrators.** Keep a persistent planning agent for each project, then split larger outcomes into focused worker tasks.
- **Live workflow state.** Follow sessions across Working, Needs you, In review, and Ready to merge using durable session, pull request, CI, and review facts.
- **Chat and terminal interfaces.** Use structured native Chat where the selected harness supports it, or retain the agent's own terminal interface.
- **Pull requests and reviews.** Inspect CI and mergeability, run interactive agent reviews, and return requested changes to the worker that owns the task.
- **Agent-controlled previews.** Open a worker's app beside its session and let the agent inspect or interact with that isolated browser surface.
- **Desktop and mobile access.** Supervise the same daemon-backed sessions through the Electron desktop app and the mobile client.
- **Multiple coding harnesses.** Use installed agents such as Claude Code, Codex, Cursor, OpenCode, Pi, OMP, and other supported harnesses, with local account and login controls where supported.

See [the current status](docs/STATUS.md) for the complete shipped capability list and explicit in-flight boundaries.

## Local and remote machines

The desktop app starts and supervises a loopback-only AO daemon for local work. All Hosted AO state is isolated under `~/.ao/hosted`, so it can coexist with an upstream Agent Orchestrator installation.

For remote work, the machine runs the same loopback-only daemon behind the separate `ao vm serve` gateway. The gateway exposes only the authenticated API, event, and terminal routes the desktop needs.

Pairing is the default way to add any machine. A single `ao-pair://` string carries address hints, a certificate fingerprint, and a passcode. The desktop races the addresses and pins the machine identity to its certificate.

Pair mode uses self-signed TLS, fingerprint pinning, passcode verification, and per-source lockout. It does not require an account, domain, or DNS, and it never exposes the daemon's loopback-only control routes.

## HAO machine management

`hao` is the standalone machine-management CLI for Hosted AO. Its boundary is deliberately separate from AO's session orchestration commands and daemon internals.

The current Linux release includes these read-only commands:

- `hao version` for stable human-readable and JSON build information.
- `hao config path`, `hao config show`, and `hao config validate` for the versioned Hosted AO machine configuration.
- `hao status` for desired state, bounded host observations, component compatibility, and proven configuration drift.
- `hao doctor` for host, permissions, disk, package and service manager, port, tool authentication, and daemon health checks.

HAO setup, host mutation, service lifecycle management, pairing migration, gateway changes, and artifact installation are still in flight. Current machine setup continues to use the released `ao` pairing flow below.

Read [the HAO machine-management boundary](docs/hao-machine-management-boundary.md) and [the v1 contract baseline](contracts/hao/v1/README.md) for ownership, compatibility, migration, and security details.

## Install the desktop app

Download Hosted AO from the [GitHub Releases page](https://github.com/agentlab-in/hosted-ao/releases). The desktop app is the canonical install path and owns its update flow.

AgentLab publishes unsigned builds. Platform trust warnings are expected. macOS releases include both a `.dmg` for first installation and a `.zip` for the updater feed.

The final npm release, `@aoagents/ao@0.10.0`, remains available only as a legacy on-ramp for existing CLI users. It is frozen and is not the recommended way to install Hosted AO.

## Pair a machine

On a 64-bit Debian-family Linux machine with systemd, run:

```bash
curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh
```

You need root access or passwordless `sudo`. The installer detects the architecture, verifies the released binary checksum, installs `ao`, provisions or reuses pair mode, and prints an `ao-pair://` string.

Treat that string as a credential. Paste it into **Add machine** in the desktop app, then clear it from any clipboard or history where it may remain. Rotate the pairing credential if it is disclosed.

The machine can be reached by private address, public address, or domain. Its certificate fingerprint is the durable identity; addresses are connection hints rather than trust anchors.

## Build from source

Source builds are for contributors:

```bash
npm install
cd frontend && npm install && npm run make
```

See [the development guide](docs/development.md) for prerequisites, platform commands, tests, and troubleshooting.

## Documentation

| Document                                                                   | Purpose                                                                                           |
| -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| [Architecture](docs/architecture.md)                                       | Backend mental model, lifecycle, persistence, status derivation, and daemon boundaries.           |
| [Backend code structure](docs/backend-code-structure.md)                   | Package ownership and the location of each backend concern.                                       |
| [CLI reference](docs/cli/README.md)                                        | AO CLI behavior and daemon route mapping.                                                         |
| [Current status](docs/STATUS.md)                                           | Shipped behavior and work that remains in flight.                                                 |
| [HAO machine-management boundary](docs/hao-machine-management-boundary.md) | HAO responsibilities, migration phases, security rules, and delivery status.                      |
| [HAO v1 contracts](contracts/hao/v1/README.md)                             | Versioned configuration, compatibility, errors, legacy installation, pairing, and gateway policy. |
| [Development guide](docs/development.md)                                   | Prerequisites, build steps, tests, and local troubleshooting.                                     |
| [Upstream merge playbook](docs/upstream-merge-playbook.md)                 | How this fork tracks upstream without losing the hosting layer.                                   |
| [Upstream product docs](https://aoagents.dev/docs)                         | Agent setup and broader day-to-day Agent Orchestrator usage.                                      |

## Relationship to upstream

Hosted AO tracks [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) as a fork. Upstream product changes are integrated while remote-machine transport and HAO remain explicit fork-owned layers.

## Anonymous telemetry

AO records privacy-preserving usage and reliability metrics designed to exclude project content and most PII. Project owner identity may be recorded to understand adoption, but repository names, paths, and URLs are not.

Read [the telemetry documentation](docs/telemetry.md) for the exact data, safeguards, and opt-out controls.

## License

Licensed under the [Apache License 2.0](LICENSE), the same license as upstream.
