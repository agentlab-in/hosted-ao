# Hosted AO v1: accounts, registered machines, remote agents

## Goal

Let a user log into AO with Google, connect a cloud VM they own and pay for, and
run coding agents on that VM from the desktop app. The VM is prepared once with
`ao setup-vm`, which binds it to the user's AO account through a device code,
installs the daemon and a TLS gateway on a domain the user owns, and registers
the machine so it appears in the app automatically. Harness login and git
credentials are separate explicit steps on the VM.

Shipped means: log in, pick a machine, add a project by git URL, run a coding
agent on it.

This supersedes the env-var pairing slice in
`docs/superpowers/specs/2026-07-26-hosted-remote-daemon-design.md`. That design's
transport findings still hold; its `AO_REMOTE_URL` / `AO_REMOTE_TOKEN` mechanism
and its shared `ao_hosted_pair` secret are replaced here.

## Scope

### In scope

- A new control plane at `ao.agentlab.in`: Google login, RFC 8628 device
  authorization, a machines registry, EdDSA signing keys, JWKS, and token
  issuance.
- `ao vm serve`: a public TLS gateway inside the `ao` binary that verifies AO
  tokens and reverse-proxies to the loopback daemon.
- `ao setup-vm`: preflight, dependency install, systemd units, account binding.
- `ao whoami` and `ao vm setup-harness claude`.
- `cloneUrl` support on `POST /api/v1/projects`, so a project can be added by
  git URL on any machine.
- Desktop: AO login, a machine list, machine switching, and remote transport
  authenticated by AO tokens.
- `docs/adr/0002-hosted-public-gateway.md`, amending the AGENTS.md network-bind
  hard rule.

### Explicitly out of scope

- Moving sessions between machines, backing sessions up to AO Cloud, and
  VM-to-VM transfer. Parked, not dead. If a VM is destroyed, whatever was on it
  is lost.
- AO provisioning VMs or issuing subdomains. The user brings a VM and a domain
  and makes their own DNS change.
- Connecting by bare IP. TLS requires a hostname.
- A GitHub App. The user's own GitHub identity via `gh auth login` is v1.
- Teams, sharing, roles, billing, tenant isolation.
- Remote browser-preview proxying.
- Driving `ao setup-vm` remotely over SSH from the laptop.
- Windows and macOS as VM targets. Ubuntu LTS only.
- Codex and every harness other than `claude`.

## Behavior

### The end-to-end flow

1. User provisions their own Ubuntu LTS VM and SSHes in.
2. User adds a DNS A record for a domain they own, pointing at the VM.
3. User runs `ao setup-vm --domain vm.example.com`. It preflights, installs
   dependencies, writes systemd units, and prints a device code plus a
   verification URL.
4. User opens `https://ao.agentlab.in/device` in a browser, signs in with
   Google, enters the code, and approves. `setup-vm` completes the binding,
   obtains a certificate, starts the gateway, and prints a summary of what is
   still missing.
5. User runs `ao vm setup-harness claude` and completes the harness's own login
   in the foreground. User runs `gh auth login` for private repositories.
6. User runs `ao whoami` to confirm the binding.
7. User opens the desktop app, signs in with Google, sees the machine in the
   list, selects it, adds a project by git URL, and runs an agent.

This costs one SSH session, one DNS record, and two browser round trips. It is a
one-time setup comparable to a self-hosted CI runner. "Super fast" applies to
connecting afterwards, not to the first setup.

### Control plane

- One Go service in `controlplane/`, `html/template` for pages, SQLite for
  storage, deployed on the existing Azure VM as a second Caddy site at
  `ao.agentlab.in`. Caddy remains on the control-plane box; it is removed only
  from user VMs.
- Tables: `accounts`, `machines`, `device_codes`, `refresh_tokens`.
- `accounts`: id, google subject, email, created at.
- `machines`: id, account id, name, hostname (public URL), last seen, created
  at, revoked at.
- Google OAuth client is created by hand by the operator. Its credentials are
  supplied by environment; they are never committed.
- Signing keys: EdDSA, generated at first boot, stored on disk under the service
  data directory, published at `/.well-known/jwks.json` with an active key and a
  next-key slot for rotation.
- Two pages: sign-in, and enter-device-code with an approval confirmation.

### Token contract

This contract is shared by the control plane, the VM gateway, and the desktop,
and is defined once so the three can be built in parallel.

- Access token: JWT, EdDSA, `iss` = `https://ao.agentlab.in`, `sub` = account
  id, `aud` = machine id, `exp` = 15 minutes, `iat`, `jti`.
- Refresh token: opaque, high entropy, stored hashed in `refresh_tokens`, bound
  to an account and a desktop install, long-lived, revocable.
- Transport: `Authorization: Bearer <jwt>` for REST and SSE. Browsers cannot set
  headers on a WebSocket handshake, so `/mux` continues to use a cookie, whose
  value is the same short-lived JWT rather than a shared secret. The cookie is
  Secure, HttpOnly, host-only, and installed by the Electron main process.
- Verification on the VM: signature against cached JWKS, `iss`, `aud` equal to
  this machine's id, `exp` with 60s skew tolerance, and `sub` equal to the
  single account id in the machine's allowlist.
- JWKS cache: 1 hour, with stale-if-error so a brief control-plane outage does
  not disconnect working users.

### VM gateway (`ao vm serve`)

- Binds :80 and :443 on the VM, obtains and renews a Let's Encrypt certificate
  for the configured domain via ACME, verifies the AO token on every request,
  and reverse-proxies to the loopback daemon.
- Rejects unauthenticated requests before they reach the daemon, returning 401.
- Never proxies loopback-only control routes (`/shutdown`, telemetry, mobile
  control).
- CORS preflights are answered by the gateway without a token; every actual
  request requires one.
- The daemon itself is unchanged: still loopback, still unauthenticated, per the
  existing hard rule. The gateway is a separate process from the same binary,
  which is what ADR 0002 records.

### `ao setup-vm`

- Ubuntu LTS with systemd and apt only. Anything else exits with the manual
  path.
- Preflight, before mutating anything: resolve the supplied hostname and confirm
  it points at this box's public IP, confirm 80 and 443 are reachable from
  outside, confirm sudo. On failure, print exact remediation and change nothing.
  The cloud firewall rule is the one thing the script cannot fix, so it must
  detect and instruct.
- Installs: the `ao` binary, tmux, git, gh, a systemd unit for the daemon, and a
  systemd unit for the gateway. It does not install harnesses.
- Binding: performs the RFC 8628 device flow against the control plane, prints
  the user code and verification URL, polls at the server-supplied interval,
  and on approval registers the machine and writes `~/.ao/machine.json` mode
  0600 containing machine id, account id, public URL, and issued-at.
- Idempotent and re-runnable. On an already-bound machine it prints the previous
  binding and re-binds.
- Ends with a summary of what is still missing: no harness configured, no git
  credentials, with the exact commands to fix each.

### Harness and git

- `ao vm setup-harness claude` runs the harness's own interactive login in the
  foreground. It does not scrape or script the exchange, because the harness
  prints a URL and then waits for a code to be pasted back.
- Git credentials are not wrapped by an AO command. `ao setup-vm` installs `gh`
  and its final summary tells the user to run `gh auth login`. The daemon's
  existing `gh auth token` path in
  `backend/internal/daemon/tracker_intake_wiring.go` picks the credential up
  with no further work.
- Readiness is read from the existing `ao doctor` checks and surfaced on the
  machine card in the app.

### Projects by git URL

- `POST /api/v1/projects` accepts an optional `cloneUrl`. When present, the
  daemon clones into `~/.ao/repos/<owner>-<repo>` and registers the result as a
  project.
- Available in local mode as well as remote. Same code path.
- Synchronous, with a generous timeout. Async cloning with progress needs a job
  model that does not exist yet.
- Clone failure returns exact remediation, for example "no git credentials on
  this machine, run `gh auth login`", never a raw git error.
- DTO changes go in `backend/internal/httpd/controllers/dto.go` and the
  operation registry in `backend/internal/httpd/apispec/specgen/build.go`,
  followed by `npm run api`. `openapi.yaml` and `frontend/src/api/schema.ts` are
  committed with the Go change.

### Desktop app

- Login uses the system browser with PKCE and a loopback redirect (RFC 8252).
  Google blocks OAuth in embedded webviews, so an in-app `BrowserWindow` login
  is not available.
- The refresh token is encrypted with Electron `safeStorage` and written under
  `~/.ao/`. Nothing is written to an OS default app-data location.
- After login the app lists the account's registered machines and the user picks
  one. No URL and no token is ever typed into the app.
- "This Mac" is always present as machine zero. **Local use never requires an
  account.** Login is required only to reach a remote machine.
- One active machine at a time. Switching machines re-points the renderer at the
  new base URL. Multiple machines side by side is out of scope.
- An unreachable machine shows an offline state with last-seen, and never
  triggers local-daemon spawning.
- A machine that is registered but has no harness configured shows exactly which
  command to run rather than failing silently.
- `AO_REMOTE_URL` and `AO_REMOTE_TOKEN` keep working unchanged for the whole
  build. They are removed only in the final batch, once the login path works end
  to end and has been verified on a fresh VM. Until then they are the way remote
  mode stays testable. Do not delete them early.
- After that removal the remaining development hatch is `AO_CONTROL_URL`, which
  points the app at a locally-run control plane. It selects which control plane
  to trust; it can never skip authentication.

### Failure behavior

- Missing, expired, or wrong-audience token: gateway returns 401, the daemon
  sees nothing.
- Control plane unreachable while a user is already connected: existing access
  tokens keep working until expiry, and the VM serves from its stale JWKS cache.
- Control plane unreachable at login: the app reports it plainly and offers
  local mode, which does not need the control plane at all.
- DNS or certificate failure during `setup-vm`: preflight catches it before any
  mutation, with remediation text.
- Device code expiry: `setup-vm` reports it and offers to restart the flow.

## Design and approach

- `controlplane/` is a new top-level directory in this repo, deployed
  separately. One checkout, one CI, one review flow for the workers building it.
- The VM gateway lives in the existing `ao` binary as a Cobra command, following
  `backend/internal/cli/*.go` conventions, and is run by systemd as a separate
  process from the daemon.
- The daemon's HTTP surface changes only by the `cloneUrl` field. Its loopback
  bind and lack of auth are untouched.
- The frontend keeps its single configured API base URL. Remote mode changes
  base selection and credential attachment, not the client protocol, exactly as
  the phase 1 design established.

## Open decisions deferred to build time

- Access token lifetime may move between 10 and 30 minutes after measuring
  refresh chatter.
- Machine display names: derived from hostname initially, renameable later.
- Copy and iconography for machine offline and no-harness states.
- Whether machine revocation lands in v1's UI or only as a control-plane
  endpoint.

## Task breakdown

Batches run in order. Tasks inside a batch are independent and can be built in
parallel by separate workers.

### Batch 1

1. **Control plane skeleton.** `controlplane/` Go service, SQLite schema for
   the four tables, config loading, health endpoint, local run instructions.
   Also writes the token contract section as a committed reference so batches 2
   and 3 can build against it without waiting.
2. **ADR 0002, hosted public gateway.** Records why the `ao` binary may bind
   :443 on a user VM, amends the AGENTS.md hard rule, and states what stays
   loopback-only.
3. **`cloneUrl` on `POST /api/v1/projects`.** Daemon-side clone into
   `~/.ao/repos/<owner>-<repo>`, remediation-shaped errors, DTO plus spec
   regeneration, tests. Independent of everything else in this spec.

### Batch 2

4. **Control plane: Google login.** OAuth code exchange with PKCE, `accounts`
   upsert, browser session, sign-in page.
5. **Control plane: keys and tokens.** EdDSA key generation and storage, JWKS
   endpoint, access and refresh token issuance and rotation, revocation.
6. **`ao vm serve`.** ACME TLS, JWT verification against a JWKS URL, machine
   allowlist check, reverse proxy to loopback, blocked control routes, CORS
   preflight handling.

### Batch 3

7. **Control plane: device flow and registry.** RFC 8628 endpoints, the
   enter-code and approval pages, machine registration, and the authenticated
   list-machines API the desktop consumes.
8. **`ao setup-vm`, part one.** Preflight, dependency install, systemd units for
   daemon and gateway, idempotency, final missing-pieces summary.
9. **Desktop: AO login.** System-browser PKCE loopback flow, refresh token
   encrypted under `~/.ao/`, signed-in state, sign-out.

### Batch 4

10. **`ao setup-vm`, part two.** Device binding, `machine.json`, re-bind
    behavior, and `ao whoami`.
11. **`ao vm setup-harness claude`.** Foreground interactive harness login, and
    surfacing readiness through `ao doctor`.
12. **Desktop: machine list.** Machine picker, "This Mac" as machine zero,
    switching, offline state, no-harness state.

### Batch 5

13. **Desktop: authenticated remote transport.** Bearer tokens on REST and SSE,
    JWT-valued cookie for `/mux`, and silent refresh. The `AO_REMOTE_URL` /
    `AO_REMOTE_TOKEN` path stays working alongside it.
14. **Fresh-VM end-to-end verification and docs.** Run the full flow on a clean
    Ubuntu VM, update `deploy/hosted/README.md` and `hosted-log.md`, record what
    was verified.
15. **Retire the env-var pairing path.** Only after task 14 passes: remove
    `AO_REMOTE_URL` / `AO_REMOTE_TOKEN` and the `ao_hosted_pair` shared secret,
    leaving `AO_CONTROL_URL` as the development hatch. Sequenced last on
    purpose, so remote mode is never untestable mid-build.
