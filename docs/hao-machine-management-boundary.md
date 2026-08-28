# `hao` machine management and the AO daemon boundary

Status: Proposed

Audience: Hosted AO maintainers, AO upstream maintainers, release operators, and implementation workers

Scope: machine preparation and lifecycle only; this document does not authorize runtime or API changes

## 1. Decision summary

Hosted AO will introduce `hao` as the command-line owner of machines controlled by the user: this computer and user-owned remote or cloud machines. `hao` prepares a machine, records declarative machine configuration, manages the services that host AO, and diagnoses that hosting stack. It does not orchestrate coding agents.

The product boundary is:

```text
ao   = projects, sessions, agents, worktrees, reviews, and the local daemon API
hao  = installation, machine configuration, host services, and remote exposure
```

The AO daemon remains an unauthenticated loopback service bound to `127.0.0.1`. `hao` must not add a configurable daemon bind host. Any off-box connection terminates at a separate Hosted AO gateway, which owns authentication, transport security, and a deny-by-default route policy before proxying permitted application traffic to the daemon.

Delivery follows three deliberate stages:

1. build the `hao` CLI and local-machine preparation;
2. make the existing pair-mode gateway the default remote-machine path;
3. add domain-based trusted HTTPS and ACME later.

This supersedes the discussion draft's suggestion that `hao` might eventually expose `spawn`, `session`, or `send`. Those commands remain exclusively in upstream `ao`.

## 2. Goal, users, and terminology

### Product goal

A user should be able to take a supported machine from a known starting state to an AO-ready state with one inspectable, repeatable tool. The same tool should explain what it changed, remain safe to run again, support automation without embedding secrets in configuration, and leave the AO daemon's core trust boundary unchanged.

### Users

- A local desktop user preparing this computer for AO.
- A technical user preparing a Raspberry Pi, spare computer, homelab server, or cloud VM they control.
- An operator using SSH and a non-interactive configuration file to reproduce a machine.
- Hosted AO desktop, which selects a local, paired, or (later) account-registered machine and connects using the transport appropriate to that origin.

`hao` is not a fleet-management product and has no authority over machines belonging to an employer, organization, or Hosted AO service operator unless the invoking user controls that machine.

### Canonical terms

| Term | Meaning |
| --- | --- |
| AO daemon | The upstream Go process serving the AO application API, terminal mux, events, and local control routes on loopback. |
| local machine | The machine running Hosted AO desktop and its supervised AO daemon. |
| managed machine | A local or remote user-controlled host whose desired hosting state is managed by `hao`. |
| gateway | The separate `ao vm serve` process (eventually a separately packaged Hosted AO component) that authenticates and proxies off-box traffic to the loopback daemon. |
| pair mode | Account-free gateway mode using self-signed TLS, a pinned SHA-256 certificate fingerprint, and an 8-character passcode. |
| hosted/domain mode | The existing account/JWT/ACME gateway mode. It remains compatible but new domain onboarding is deferred. |
| preparation | Installing/verifying binaries, directories, and service definitions without authenticating third-party tools. |
| initialization | Applying desired configuration, checking credentials, starting services, and producing connection material. |

### Explicit non-goals

- No orchestration aliases or wrappers in `hao`: no `hao spawn`, `hao session`, `hao send`, project CRUD, PR actions, or terminal attachment.
- No organizations, teams, RBAC, SSO, shared credentials, machine pools, policy distribution, or unattended third-party account creation.
- No managed cloud infrastructure or authority over machines the user does not control.
- No public bind, TLS, authentication, certificate lifecycle, or machine registry inside the AO daemon.
- No domain/ACME implementation in the first delivery phase.
- No DNS-01, private CA, bring-your-own certificate, reverse-proxy integration, tunnel provider, or mDNS requirement in v1.
- No silent upgrades of harnesses, Git, `gh`, or package-manager-owned dependencies.
- No removal of daemon capabilities required for correct local orchestration merely because Hosted AO once added or modified them.

## 3. Strict responsibility boundary

| Component | Owns | Must not own |
| --- | --- | --- |
| `hao` | Desired machine configuration; prerequisite planning and installation; privilege escalation handoff; service install/start/stop/restart/status/log discovery; local preparation; remote pair provisioning; config validation; migration of legacy Hosted AO host state; host-level diagnostics; version compatibility reporting. | Sessions, agents, worktrees, projects, PRs, terminal traffic, daemon business logic, public request authentication, desktop UI state, control-plane accounts. |
| upstream `ao` CLI | Thin client commands for AO orchestration and daemon-local control; daemon launch/status where upstream requires it; user-visible orchestration errors. | Machine provisioning, public exposure, TLS/ACME, remote-machine registry, privileged dependency installation as a hosting workflow. |
| AO daemon | AO domain services and durable facts; project/session/review lifecycle; local system capability reporting needed to run sessions; app API, SSE, mux; loopback health/readiness; shared local repair APIs when the desktop requires them. | General network exposure, gateway credentials, machine identity, certificate issuance/pinning, account selection, remote endpoint selection, system service definitions. |
| Hosted AO gateway/pairing | The only remote ingress; TLS; passcode or machine-audience-token verification; lockout; CORS for the exposed endpoint; request limits; credential stripping; deny-by-default route policy; proxying to the currently discovered loopback daemon. | AO session semantics, SQLite access, dependency installation, desktop pin persistence, account UI, loopback-only control routes. |
| Hosted AO desktop | Local daemon supervision; machine list, origin labels, selection, reachability; account login for legacy/domain machines; pair-string paste/race; certificate pin enforcement for REST/SSE/mux; encrypted credential persistence; desktop self-update UX. | Privileged host mutation, system service installation, gateway server duties, AO domain logic. |

Two nuances are load-bearing:

1. Capability detection belongs with the component that knows the capability. The daemon may continue to report whether Git, a runtime, or an installed harness is usable because session correctness depends on those facts. `hao doctor` may aggregate that report with host/service/gateway checks; it must not create a divergent definition of AO readiness.
2. The existing Connect Mobile listener is not the general remote-machine gateway. It is an explicit, authenticated secondary listener owned by the daemon for the mobile product. It remains until that product has a replacement, and it must not be widened or reused by `hao`.

## 4. Proposed v1 CLI

All commands support `--json` where output is consumed by automation. Mutating commands support `--config <path>`, `--non-interactive`, and `--dry-run`. In non-interactive mode, missing required input is an exit-2 usage/configuration error; `hao` never prompts.

### `hao setup`

Prepare this machine for Hosted AO.

```text
hao setup [--config hosted-ao.yaml] [--non-interactive] [--dry-run]
          [--install missing|none] [--yes]
```

Semantics:

- Detect the OS, architecture, init system, current user, package managers, and existing AO/`hao` installations.
- Resolve a plan before mutation and print it in interactive mode.
- Verify/install the `hao`-owned AO daemon/gateway artifact and hard host dependencies.
- Verify a selected harness and optionally invoke its documented installer. Authentication is not part of setup.
- Create the state/config tree with least-privilege ownership.
- Install service definitions, but do not expose a remote listener unless the config explicitly requests pair mode and initialization succeeds.
- Re-running an already-satisfied setup is a successful no-op. A version change updates only `hao`-owned artifacts and service definitions.

`--install none` is the audit-only path. `--install missing` may use a supported package manager. `--yes` accepts the already-printed plan; it does not consent to arbitrary scripts or third-party login.

### `hao init`

Apply configuration and make a prepared machine usable.

```text
hao init [--mode local|pair] [--config hosted-ao.yaml]
         [--non-interactive] [--dry-run]
```

- `local` validates the AO daemon and selected harness, writes configuration atomically, and enables/starts the local daemon service if service management was selected.
- `pair` additionally provisions the gateway's persistent self-signed certificate and hashed passcode, enables daemon and pair-gateway services in dependency order, verifies the gateway locally, and prints the existing `ao-pair://v1/...` secret exactly once.
- `https` is rejected in v1 with a stable `feature_deferred` error and a pointer to pair mode. Existing legacy hosted/domain services are detected and preserved, not rewritten.
- Harness and GitHub authentication are verified, not automated. Interactive mode may offer to launch the harness's own login command or `gh auth login`, with the exact command shown and explicit consent.

`setup` and `init` remain separate: privileged, reproducible host mutation can be completed by an administrator, while unprivileged credentials and activation remain with the target user. A convenience `hao setup --init` may be considered later without merging their contracts.

### `hao status`

Read-only summary of desired and observed state:

- configuration version and mode;
- installed component versions and compatibility;
- daemon/gateway service enabled, active, PID, and restart state;
- loopback health/readiness and gateway reachability;
- credential presence/validity without printing secrets;
- selected harness binary/auth state and GitHub auth state;
- drift between config, files, and service definitions.

An inactive intentionally-disabled service is not an error. Exit 0 means observations completed; `--strict` exits 1 when desired state is unhealthy or drifted.

### `hao doctor`

Run deeper diagnostics. It combines:

- host checks owned by `hao` (platform, disk, permissions, package manager, init system, ports, service files, state ownership, gateway certificate/passcode stores);
- AO readiness from the daemon/its shared doctor implementation;
- harness and GitHub credential probes with explicit timeouts;
- pair-mode end-to-end tests through the gateway while asserting blocked paths remain inaccessible.

Default output redacts tokens, passcodes, pairing strings, environment values that look secret, and private key material. `--fix` is not part of v1; remediation commands are printed, and mutation remains explicit through `setup` or `init`.

### `hao config`

```text
hao config path
hao config show [--effective]
hao config validate [--file path]
hao config set <supported-key> <value>
```

`show` redacts secret references. `set` accepts only schema-defined keys, locks the file during update, writes a same-directory temporary file, fsyncs where supported, renames atomically, and preserves the last-known-good backup. Unknown keys fail closed so a typo cannot silently leave a host exposed differently than intended.

### Lifecycle commands

V1 should include `hao start`, `hao stop`, `hao restart`, and `hao logs` because machine management is incomplete without service operation. They act only on `hao`-managed daemon/gateway services, respect dependency order (daemon before gateway on start; gateway before daemon on stop), and report manual commands on platforms without a supported service manager.

### Update ownership

`hao update` is deferred until artifact provenance, channels, rollback, and component compatibility are specified. Until then:

- Hosted AO desktop continues to update itself through its existing Electron updater/release process.
- `hao` may replace `hao`-owned AO daemon/gateway binaries only when the operator explicitly reruns a versioned installer or `hao setup` with an artifact version.
- Git, `gh`, package managers, and harness CLIs remain user/package-manager owned. `hao` reports versions and remediation; it does not upgrade them.
- Service files may be reconciled by `hao setup`, with a backup and validation before restart.

## 5. Configuration and state contract

Proposed non-secret configuration:

```yaml
version: 1
machine:
  name: build-box
mode: pair
components:
  aoVersion: "0.14.0"
harness:
  id: claude-code
install:
  dependencies: missing
service:
  enabled: true
pair:
  listenPort: 443
```

Rules:

- Configuration describes desired state and contains no passcode, private key, OAuth token, harness credential, GitHub token, or pairing string.
- Environment/flag precedence is `flags > environment overrides > config file > defaults`; `hao config show --effective` reports each value's source.
- A schema version is mandatory. New binaries may read older versions and migrate through an explicit backup; they refuse unknown future versions.
- Config is user-readable only when it contains sensitive topology. Credential and private-key files are user-only regardless.
- Machine identity is not an IP address. In pair mode it is the certificate fingerprint; address entries are replaceable hints.

The current Hosted AO root is `~/.ao/hosted`, derived in Go by `config.StateRootSegments()` (`backend/internal/config/config.go`) and mirrored by `STATE_ROOT_SEGMENTS` (`frontend/src/shared/state-root.ts`). Existing defaults put `running.json` at the root and daemon data below `data`; explicit `AO_RUN_FILE` and `AO_DATA_DIR` overrides continue to win. Electron pins `userData` below this root rather than using OS application-data defaults.

V1 should keep this root to avoid breaking installations. Proposed additions are:

```text
~/.ao/hosted/
  hao/config.yaml
  hao/backups/
  bin/                         # only if using a per-user install
  data/                        # existing daemon durable data
  running.json                 # existing daemon discovery file
  machine.json                 # legacy hosted gateway config, compatibility only
  vm-gateway/pair-cert/        # existing pair identity
  vm-gateway/pair-passcode/    # existing hashed credential
  electron/                    # desktop userData; desktop-owned
```

`hao` must use the shared state-root helper rather than respell `.ao/hosted`. A system service must receive absolute `AO_DATA_DIR` and `AO_RUN_FILE` paths and run as the target unprivileged user.

## 6. Idempotency, privileges, installation, and errors

### Idempotency and transactions

- Every mutation starts from an observed-state plan and records whether each step is create, update, no-op, or blocked.
- File writes are atomic; existing files are backed up before schema or service-definition changes.
- Certificate and passcode stores are never regenerated by ordinary reruns. Rotation requires a dedicated explicit command in the pairing phase.
- Service enable/start operations are idempotent. A running healthy matching service is not restarted.
- If a later step fails, `hao` rolls back newly written service files/config and disables only services it enabled during that invocation. It does not uninstall pre-existing packages or delete existing AO data.
- A crash leaves a transaction journal sufficient for the next run to explain and finish or roll back the incomplete operation.

### Privilege model

- Run orchestration workloads, the daemon, and gateway as the target non-root user. Existing `setup-vm` correctly treats root-owned agent sessions as unsafe (`backend/internal/cli/setupvm_plan.go`, `rootSetupUserProblem`). Preserve that invariant.
- Elevate only the small steps that require it: system package installation, writes under system service directories or a system binary directory, privileged-port capability/binding, and service-manager operations.
- Never run a harness login, `gh auth login`, Git operation, or agent session under `sudo`.
- Print the exact elevated plan first. Prefer a narrowly generated helper/argv allowlist; never interpolate configuration into a shell command.
- Linux can use systemd in v1. macOS local setup may rely on desktop supervision initially; launchd support requires its own acceptance work. Windows uses ConPTY for sessions but needs a separately designed service lifecycle before unattended hosting.

### Platforms and dependencies

| Platform | Local preparation v1 | Remote pair host v1 | Notes |
| --- | --- | --- | --- |
| Ubuntu LTS x64/arm64 | Supported | Supported | Primary service target: systemd; package manager apt; real-VM acceptance required. |
| macOS current + previous, arm64/x64 | Supported | Manual/experimental | Desktop owns its local daemon; Homebrew may be offered but never assumed. Remote service needs launchd design before supported. |
| Windows 11 x64 | Supported for local AO | Not supported initially | No tmux requirement because AO uses ConPTY (`systemcheck.checkTmux`); remote Windows service/gateway lifecycle is deferred. |
| Other Linux distributions | Inspect/manual | Unsupported | `hao doctor` may report compatible binaries but must not claim managed service support. |

Hard prerequisites are Git, the platform terminal runtime (tmux on Unix or ConPTY on Windows), and at least one supported harness. `gh` is advisory for AO readiness today: `systemcheck.checkGH` deliberately sets `Required: false` because sessions can run without GitHub CLI. `hao` must not turn `gh` into a universal blocker; it becomes required only for a config/profile whose workflows explicitly require GitHub.

The current daemon installer service permits only `tmux`, `gh`, `claude`, `codex`, `opencode`, and `copilot`, builds fixed argv, tracks bounded async jobs, and refuses privileged Linux execution (`backend/internal/service/systeminstall/systeminstall.go`). `hao` should reuse the same install-plan vocabulary initially, then own privileged host execution outside the daemon. It must retain a closed target allowlist and platform-specific fixed argv.

### Error contract

- Exit 0: requested operation completed or desired state already held.
- Exit 1: operational failure, unhealthy desired state under `--strict`, authentication failure, or rollback required.
- Exit 2: invalid CLI usage, invalid/unsupported config, missing non-interactive input.
- Exit 3: privilege required but unavailable/refused.
- Exit 4: unsupported platform or deferred feature.

JSON errors contain `code`, `message`, `component`, `operation`, `remediation`, and a stable `details` object. Human output includes the failed step, what changed before failure, rollback result, and a safe rerun command. No error includes a bearer token, passcode, pairing fragment, environment dump, private-key path contents, or command output likely to contain credentials.

## 7. Current code evidence and disposition

The inventory below is based on current `origin/develop`, existing ADRs/specs, and Git history. “Hosted AO addition” means downstream work or an adjacent surface built for Hosted use; it does not imply the capability should be removed from the daemon.

| Current item and evidence | Disposition | Reason and target state |
| --- | --- | --- |
| Loopback-only bind: `config.LoopbackHost`, `Config.Load`, and absence of an `AO_HOST` override in `backend/internal/config/config.go`; primary server wiring in `backend/internal/httpd/server.go`. | **Keep in AO daemon** | Core security invariant. `hao` configures discovery paths/ports but cannot widen the bind. |
| Shared local health runner `doctor.Run` and `doctor.Deps` in `backend/internal/doctor/doctor.go`, used by `ao doctor` in `backend/internal/cli/doctor.go` and `GET /api/v1/doctor` in `backend/internal/httpd/controllers/doctor.go`. | **Keep in AO daemon/upstream `ao`** | Remote and local clients need one truthful AO-readiness definition. `hao doctor` aggregates it rather than forks it. |
| Startup requirements `systemcheck.Service.CheckStartup`/`Check` and `Requirement` in `backend/internal/service/systemcheck/systemcheck.go`, exposed by `backend/internal/httpd/controllers/system.go`. | **Keep in AO daemon** | Git/runtime/harness availability affects whether AO can start sessions and must remain visible to desktop. `gh` remains advisory. |
| Host mutation API and jobs in `backend/internal/service/systeminstall/systeminstall.go`, ports in `backend/internal/ports/system.go`, controller in `backend/internal/httpd/controllers/systeminstall.go`, routes under `/api/v1/system/install/{target}`. | **Move privileged installation to `hao`; keep a compatibility/local-repair window, then reassess/remove daemon mutation routes** | Desktop one-click local repair currently depends on fixed allowlisted jobs. New machine provisioning must not expose privileged mutation through the daemon. During migration the route remains loopback-only and is already blocked from LAN/gateway; desktop should eventually invoke a `hao` host-operation boundary or show manual remediation. Read-only requirements stay in daemon. |
| Connect Mobile bridge state in `backend/internal/mobilebridge`, `LANManager`/`NewMobileLAN` in `backend/internal/httpd/lan_listener.go`, auth/lockout in `backend/internal/httpd/auth.go`, control routes `/api/v1/mobile/*`, and restore wiring in `backend/internal/daemon/daemon.go`. | **Keep in AO daemon until a separate mobile migration** | It is active product behavior and shares the daemon API deliberately. It is not `hao` remote transport. Preserve explicit enablement, `0.0.0.0` only while enabled, bearer auth, lockout, plaintext/home-network warning, and blocked control/install routes. |
| LAN blocklist in `backend/internal/httpd/lan_listener.go`: `/shutdown`, `/internal`, `/api/v1/mobile`, `/api/v1/dev`, `/api/v1/system/install`. | **Keep in AO daemon** | Required safety boundary for Connect Mobile; additions must be mirrored when sensitive loopback routes appear. |
| `ao setup-vm` and plan/bind/systemd implementation in `backend/internal/cli/setupvm*.go`. | **Move to `hao`; remove `ao setup-vm` after migration** | It installs host dependencies, creates systemd units, chooses target user, writes `machine.json`, and binds account/device state: machine management, not orchestration. Preserve its preflight, absolute path, non-root service, port, rollback, and ownership lessons. |
| `ao vm serve` and `ao vm setup-harness` in `backend/internal/cli/vm.go`. | **Move command ownership to `hao`/gateway package; retain a hidden/service compatibility entry point temporarily** | Gateway process lifecycle and harness preparation belong to machine management. Existing systemd `ExecStart` lines must keep working throughout the compatibility window. The reusable gateway code may remain in the repository. |
| Pair provisioning and display in `backend/internal/cli/pair.go`; shared codec/vectors in `backend/internal/pairstring`; pair flags/plans in `setupvm_plan.go`. | **Move user-facing commands to `hao`; keep codec shared with desktop** | `hao init --mode pair` becomes the normal entry point. A `hao pair show`/`rotate` subcommand may expose recovery. The grammar must remain golden-vector compatible with `frontend/src/shared/pair-string.ts`. Keep `ao pair` as a compatibility alias before removal. |
| Hosted gateway implementation in `backend/internal/vmgateway`: `Server`, `NewHandler`, `NewPairHandler`, `denyByDefault`, `requireToken`, `requirePasscode`, `PairCertificate`, `PasscodeStore`, machine/JWKS config. | **Move to gateway ownership, not daemon; keep implementation until packaging is deliberately split** | It already runs as a separate process and owns TLS/auth/proxy policy correctly. Command/binary packaging may move under `hao`, but never fold it into the daemon. |
| Gateway allowlist/blocked paths in `backend/internal/vmgateway/proxy.go`, including exclusion of `/shutdown`, telemetry, mobile control/devices, developer maintenance, and system install. | **Keep in gateway and test as a contract** | Public safety depends on deny-by-default routing. Every new daemon route requires explicit classification before exposure. Credentials are stripped before loopback proxying. |
| Hosted account control plane and machine registry described in `docs/STATUS.md` and implemented under `controlplane/`; desktop token sources `frontend/src/main/ao-control-token.ts` and `ao-machine-token.ts`. | **Keep for existing hosted users; move future domain/account policy outside daemon; do not require for pair mode** | Legacy/domain machines need compatibility. Pair mode never contacts the control plane. Retirement of JWT/account machinery is a separate product decision after migration telemetry and export paths exist. |
| Desktop registered-machine controller and selection in `frontend/src/main/ao-machines.ts`, `machine-selection.ts`, `machine-transport.ts`; paired registry/transport/pinning in `paired-machines.ts`, `paired-machine-transport.ts`, and `paired-machine-cert.ts`. | **Keep in Hosted AO desktop** | Endpoint selection, credential/pin storage, REST/SSE/mux transport, and origin labels are client responsibilities. `hao` produces connection material but never chooses the desktop's active machine. |
| Pair-string parser/racing in `frontend/src/shared/pair-string.ts` and `pair-race.ts`, with e2e coverage in `frontend/e2e/pair-string.spec.ts`. | **Keep in desktop/shared contract** | Address hints are raced by the client; fingerprint is identity. Maintain shared vectors with Go. |
| State-root fork: `config.StateRootSegments()`/`StateRootSubdir` and `frontend/src/shared/state-root.ts`; Electron userData guard tests. | **Keep while Hosted AO and upstream installations can coexist** | Prevents collision with upstream `~/.ao`. `hao` must consume, not duplicate, the helper. Removal would be destructive and is not part of the migration. |
| Desktop updater in `frontend/src/main/auto-updater.ts`, update settings/telemetry, `.github/workflows/frontend-release.yml`, guards/e2e, and `frontend/docs/desktop-release.md`. | **Keep in desktop/release pipeline** | Desktop updates itself. `hao` must not become a second desktop publisher. AgentLab builds remain unsigned per repository policy; macOS zip/feed remain required alongside dmg. |
| CLI installer `install.sh` and landing copy/scripts under `frontend/src/landing/public/cli/install.sh`, plus release Linux `ao` artifacts. | **Move installer intent to `hao`, preserve old URL/flow during compatibility** | New installs should install `hao`, then let it obtain version-pinned AO/gateway artifacts and run pair setup. Existing `curl | sh` → `ao pair` users need an overlapping release and rollback path. |
| Desktop local daemon supervisor and state-root/userData wiring under `frontend/src/main` and app entrypoint. | **Keep in desktop** | Local app lifecycle is a desktop concern. `hao` may diagnose/reconcile an optional service, but must not create two competing supervisors. |

### What Hosted AO added adjacent to the daemon

The downstream inventory is therefore broader than the daemon itself:

- doctor's remote-readable route and shared framing;
- startup requirement/install endpoints used by desktop setup;
- the explicitly opt-in mobile LAN bridge and its control surface;
- hosted VM setup commands, machine binding, systemd plans, and gateway;
- hosted JWT/ACME mode and account control plane;
- pair-mode certificate, passcode, lockout, pairing-string, and installer;
- desktop account and paired machine registries, authenticated transports, pinning, address racing, and machine selection;
- the isolated `~/.ao/hosted` state root and release/update additions.

Git history also shows major upstream changes merged into `develop`; names such as “doctor” or “system requirements” are insufficient evidence of exclusive Hosted ownership. The disposition above follows runtime responsibility and current consumers.

## 8. Security and threat boundaries

### Core invariant

The daemon is loopback-only and unauthenticated. This is safe only because neither `hao` nor service files can point it at a non-loopback address. The gateway is a separate process and the only general off-box ingress.

### Privileged installation

Threats include configuration-to-shell injection, compromised download artifacts, privilege persistence, wrong-user ownership, and running agent credentials as root. Mitigations:

- fixed install targets and argv, no `sh -c` with user data;
- HTTPS download plus a release-published cryptographic checksum, verified before atomic install; signatures/provenance when the release pipeline supports them;
- display artifact source, version, digest, destination, and elevated actions before consent;
- no secrets passed on command lines; scrub environment inherited by privileged helpers;
- root-owned service definition, user-owned state and credentials, service `User`/`Group` fixed to the target non-root identity;
- refuse ambiguous target users and refuse root as the workload identity;
- preserve existing data on rollback.

### Credentials

- Harness and GitHub credentials are owned by their tools and target user. `hao` checks exit status/safe metadata only and never copies tokens into its config.
- Pair passcode plaintext is displayed once and never logged or stored on the host; only a slow/hash-appropriate stored verifier is retained. Rotation invalidates active clients.
- Pair certificate private keys are mode `0600`; losing/replacing the certificate requires deliberate re-pairing. A fingerprint mismatch has no bypass.
- Desktop secrets use Electron safe storage where available; pinned fingerprints and machine origin are security-relevant durable state, not disposable cache.
- Account refresh/control tokens and machine-audience tokens remain separated by audience. Gateway credentials are stripped before proxying.

### Public gateway restrictions

The gateway must continue to:

- authenticate every non-preflight request before proxying;
- apply per-source passcode lockout using the actual peer address, ignoring spoofed forwarding headers;
- enforce request-body and upstream-response bounds;
- terminate TLS and never downgrade pair mode to plaintext;
- deny by default, with explicit allowed API/mux/health paths;
- never proxy `/shutdown`, telemetry, mobile control/device routes, development routes, or host-mutating installation routes;
- reload loopback daemon discovery without accepting an arbitrary upstream host;
- enforce pin verification for REST, SSE, and mux at the Electron session layer.

Pair mode's out-of-band pairing string is a secret equivalent to a DSN. Clipboard/history exposure and shoulder-surfing are accepted setup-channel risks; docs must tell users to clear it and rotate if disclosed. Address hints are not identity and may be attacker-controlled; only a matching pinned certificate plus valid passcode identifies/authenticates the box.

Domain/ACME mode later still requires application authentication. A public CA proves control of a name, not authorization to use AO.

## 9. Phased delivery and acceptance

### Phase 0: contract fixtures and compatibility floor

Dependencies: none.

- Freeze config v1 schema, exit/error codes, component compatibility rules, shared state-root resolution, gateway route-policy fixtures, and pairing-string golden vectors.
- Record current service unit names, paths, and CLI entry points installed by `ao setup-vm`/`ao pair`.
- Add deprecation telemetry/counters that reveal command use without recording secrets.

Acceptance: old services and pairing strings are represented in fixtures; no runtime behavior changes; a migration can identify every installed legacy shape.

### Phase 1: `hao` CLI and local machine preparation

Dependencies: Phase 0; released `hao` artifact channel and checksum metadata.

- Implement config/status/doctor/setup/init-local and lifecycle commands.
- Reuse daemon readiness semantics; do not duplicate orchestration checks.
- Support Ubuntu, macOS local desktop, and Windows local desktop per the matrix.
- Make installation plans deterministic and dry-runnable; privilege boundary is separately testable.
- Keep desktop local supervision authoritative unless the user explicitly selects a system service model.

Acceptance: a clean supported local host becomes AO-ready; a second identical run is a no-op; non-interactive setup is reproducible; rollback restores pre-run files/services; the daemon still binds only loopback; existing desktop startup and one-click repair continue to work.

### Phase 2: pair-mode remote machines

Dependencies: Phase 1 service/install primitives, existing gateway/pair implementation, shared vectors, Linux release artifacts.

- Move `setup-vm --pair`/`ao pair` UX into `hao init --mode pair`.
- Adopt existing cert/passcode stores and units in place; do not rotate identity.
- Preserve the existing pairing string and desktop paste/race/pin flow.
- Add `hao pair show`, `hao pair rotate`, and targeted gateway logs/status if the recovery UX needs them.
- Support Ubuntu systemd x64/arm64 first; other remote OSes remain explicit unsupported/manual modes.

Acceptance: clean Ubuntu VM, Pi-class arm64 box, and already-paired legacy VM all work; first provisioning prints one usable string; rerun does not change fingerprint/passcode; blocked gateway paths return 404/deny; bad passcodes lock out; changed certificate hard-fails; daemon remains loopback-only; gateway/daemon restart and IP change preserve pairing.

### Phase 3: domain-based trusted HTTPS/ACME

Dependencies: stable `hao` update/rollback contract, decided credential/connection format, public artifact/release ownership, DNS/port validation design.

- Expose a `https` mode only after validating `domain → public IP → this machine`, TCP 80/443 reachability, ACME terms/contact, renewal, and rollback.
- Reuse the existing hosted gateway JWT/ACME implementation where it matches the chosen product policy.
- Keep pair mode the recommended fallback and never modify the daemon bind.

Acceptance: issuance and renewal succeed on a fresh supported VM; DNS mismatch/private-only/blocked-port cases fail before mutation with pair remediation; auth is required despite trusted TLS; renewal failure is visible before expiry; rollback returns to the previous working gateway certificate/config.

## 10. Migration and rollback

The migration must span at least two desktop/CLI stable release trains.

1. **Introduce without ownership transfer.** Ship `hao` able to detect/read existing `ao setup-vm`, `ao pair`, `machine.json`, pair cert/passcode, and systemd state. Existing commands and services remain unchanged.
2. **Adopt in place.** `hao setup` writes its config from observed legacy state only after showing the import plan and taking backups. It preserves certificate, passcode hash, ports, target user, state paths, unit names, and enabled/running state. No paired desktop action is needed.
3. **Dual entry-point window.** New docs use `hao`; legacy `ao setup-vm`, `ao pair`, `ao pair show`, and service `ExecStart=... ao vm serve` remain functional and print non-blocking migration guidance. Gateway binary compatibility is tested across old/new service definitions.
4. **Desktop compatibility window.** Desktop continues to understand account machines and existing pair strings/stores. It must not require sign-in for paired machines and must retain origin labels.
5. **Transfer service entry points.** A later `hao setup` updates units atomically to the new gateway/daemon entry points, validates them, daemon-reloads, starts, probes through the gateway, then deletes backups only after an operator-defined retention period. On failure it restores old units and binaries and restarts the old services.
6. **Remove aliases only after evidence.** Remove old machine-management commands from `ao` only when two stable trains have passed, supported installed versions can update, usage is negligible, release rollback is proven, and the desktop no longer calls daemon install mutation routes. Removal must carry explicit release notes and a command that explains the replacement.

Rollback is version-aware: configuration written by a newer `hao` retains its previous file and service definitions; binaries are installed side-by-side or with an atomic previous link; durable AO data is never downgraded automatically. If a daemon storage migration makes binary rollback unsafe, `hao` stops and explains the minimum compatible version rather than attempting it.

Existing hosted/account machines are preserved throughout. Domain onboarding may be hidden from new UX while `machine.json`, account selection, token minting, and JWT gateway mode remain operable for registered users.

## 11. Test and acceptance matrix

| Mode | Ubuntu x64 | Ubuntu arm64 | macOS arm64/x64 | Windows x64 | Required scenarios |
| --- | --- | --- | --- | --- | --- |
| Config/status/doctor | Unit + fresh VM | Unit + Pi/VM | Real local + unit | Real local + unit | Precedence, redaction, unknown schema, corrupt file, permissions, JSON stability. |
| Local setup/init | Fresh VM | Fresh VM | Fresh user account, desktop installed | Fresh user account, desktop installed | Clean install, missing dependency, already ready, second-run no-op, non-interactive, denied privilege, partial failure/rollback, paths with spaces. |
| Local lifecycle | systemd | systemd | Desktop supervision; launchd only when supported | Desktop supervision | Start/stop ordering, stale run file, occupied port, crash/restart, no competing supervisor. |
| Pair gateway | Fresh public VM + LAN | Pi-class or arm64 VM | Manual/experimental only | Not supported | First pair, pair show, rotate, service reboot, IP change, IPv4/IPv6 hints, wrong cert, wrong passcode/lockout, no control-plane traffic. |
| Legacy adoption | Existing hosted and paired VM | Existing paired box | Existing desktop registry/pins | Existing desktop registry | Identity/credential preserved, old service works before and after, rollback restores old unit, old desktop/new host and new desktop/old host compatibility. |
| Gateway policy | httptest + real VM | httptest | Desktop e2e | N/A | Allowed REST/SSE/mux works; shutdown, telemetry, mobile, dev, install denied; auth stripped; CORS exact; oversized body rejected. |
| Domain/ACME (Phase 3) | Fresh public VM | Fresh public VM | Client only | Client only | DNS mismatch, private IP, closed 80/443, issuance, restart, renewal, expired cert, auth required, rollback. |
| Release/update | Linux binaries/checksums | Linux binaries/checksums | Existing zip+dmg/feed policy | Existing installer/feed | Provenance/checksum failure, channel compatibility, no second desktop publisher, previous binary retained. |

CI should cover parsers, planners, fixed argv, redaction, atomic writes, service rendering, route policy, and cross-version fixtures. Real-machine acceptance is mandatory for privilege, service manager, firewall/ports, package manager, reboot, TLS, and updater behavior; mocks cannot establish those properties.

## 12. Dependency-ordered follow-up batches

Each batch is intended to be independently assignable to future AO workers and should land as small PRs against `develop`.

1. **Contract baseline:** config JSON Schema/YAML examples; exit/error code definitions; component compatibility table; legacy installation fixtures; pairing vectors and gateway route-policy manifest.
2. **Read-only CLI skeleton:** `hao version`, config path/show/validate, state-root library reuse, JSON framing, redaction tests, packaging without mutation.
3. **Host inventory and doctor aggregation:** platform/init/package detection, daemon doctor client, service/gateway checks, stable remediation output, timeouts.
4. **Install planner and privilege helper:** fixed targets/argv, artifact checksum/provenance verification, dry-run, target-user validation, atomic files, transaction journal, rollback unit tests.
5. **Local setup/init:** daemon artifact install, local config, macOS/Windows desktop-supervision coexistence, Ubuntu optional service, idempotency acceptance.
6. **Service lifecycle:** systemd renderer/manager, ordered start/stop/restart/logs, stale discovery and port-conflict diagnostics, real Ubuntu matrix.
7. **Legacy discovery/adoption:** parse existing `machine.json`, pair stores, paths, and units; backup/import/rollback; cross-version fixtures. No command deprecation yet.
8. **Pair UX migration:** `hao init --mode pair`, show/rotate/recovery; preserve cert/passcode; gateway health verification; Linux x64/arm64 e2e.
9. **Desktop compatibility release:** old/new gateway matrix, account + pair origins, pin persistence, machine switching, explicit migration messaging.
10. **Installer/release transition:** install `hao`, publish checksums/provenance, keep old installer URL and AO artifacts, document conductor ownership and rollback.
11. **Daemon mutation-route migration:** replace desktop calls to `/api/v1/system/install/*` with `hao` boundary/manual remedy, retain read-only requirements; observe compatibility window before removing routes.
12. **Legacy CLI deprecation/removal:** warnings, telemetry review, two-release gate, service entry-point conversion, then remove `ao setup-vm`/pair aliases only when gates pass.
13. **Domain/ACME product decision and threat review:** settle credential/connection format, account requirement, DNS/port policy, renewal/operator notifications, then write an implementation spec.
14. **Domain implementation:** only after batch 13 approval; reuse gateway, never daemon exposure.

## 13. Genuinely unresolved human decisions

These questions change product policy and are not silently decided here.

1. **What artifact contains `hao`, the daemon, and gateway?** Recommendation: ship a small independently versioned `hao` bootstrap binary that installs a compatibility-pinned daemon/gateway bundle. This avoids turning the frozen npm package or desktop updater into a server installer, but requires a signed/provenanced release channel.
2. **Should local Linux install a persistent systemd daemon by default?** Recommendation: no. Default local behavior should match desktop supervision; require `service.enabled: true` for headless/unattended use to avoid competing supervisors.
3. **How should macOS and Windows remote hosting graduate from experimental?** Recommendation: require dedicated launchd/Windows Service designs and reboot/upgrade acceptance before advertising support; do not emulate system services with shell startup hacks.
4. **When may daemon install mutation routes be removed?** Recommendation: after desktop repair uses `hao` or manual commands for two stable releases and route-use telemetry shows no material old-client population. Read-only requirement/doctor routes remain.
5. **Who owns AO daemon binary updates on a desktop-managed local machine?** Recommendation: desktop continues to own its bundled daemon; `hao` reports it but does not replace it. `hao` owns only explicitly headless/service installations.
6. **What authenticates later domain mode?** Recommendation: retain machine-audience JWTs for already registered machines, but conduct a separate decision on whether new self-hosted domain machines use local credentials, an account, or both. Trusted TLS alone is never sufficient.
7. **What is the domain connection-string format?** Recommendation: version a URI analogous to `ao-pair://` and make the credential a one-time import secret, but decide only with the desktop credential-store and account policy.
8. **What level of harness installation automation is supportable?** Recommendation: v1 supports a small tested allowlist and otherwise prints vendor instructions. Authentication always remains vendor-owned and user-run.

## 14. Definition of architectural success

The boundary is successful when a maintainer can remove every machine-provisioning command from upstream `ao` without changing how AO creates or supervises a session; when a gateway can be replaced without migrating AO's database; when `hao` can prepare or repair a host without learning an agent session's contents; and when disabling every remote mode leaves an ordinary loopback-only AO installation fully functional.
