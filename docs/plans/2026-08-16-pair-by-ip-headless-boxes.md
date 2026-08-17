# Pair by IP: headless AO boxes with no domain and no account

Spec resolved 2026-08-16. Interview-driven; every decision below is settled.

## Goal

People want to run the AO daemon headlessly on a box they own (a Raspberry Pi,
a homelab server, a spare machine on their LAN) and drive it from the desktop
app on their Mac. Today the only remote path is `ao setup-vm`, which requires a
domain they control with DNS pointed at the box (ACME cannot issue for a bare
IP) and an account on the ao.agentlab.in control plane. Both are walls for
someone whose box is a Pi on their own network.

This adds a second door: put in the box's IP address and a passcode, and it
works. No domain, no certificate authority, no account, no contact with the
control plane at all.

## Scope

In scope:

- A pair mode for the VM gateway: self-signed TLS, passcode auth, no ACME, no
  control plane.
- Trust-on-first-use certificate pinning in the desktop app.
- A provisioning path that installs the two systemd units on a Debian-family
  box without a domain or an account, and prints the passcode and fingerprint.
- A standalone `ao` binary for Linux, x64 and arm64. This does not exist today
  and is a hard blocker for everything else.
Already true, and requiring no work. Verified on `develop` 2026-08-17:

- **Desktop sign-in is already optional.** `AccountSection.tsx` states there is
  no sign-in wall anywhere else and its copy already reads "Everything on this
  computer works without an account". The control-plane token source returns
  without any network call when signed out, machine listing falls back to the
  local machine, and `frontend/src/main/ao-machines.test.ts:107` asserts no
  fetch occurs on a signed-out refresh. This was a founding decision, recorded
  in `docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md:183`
  ("Local use never requires an account"). An earlier draft of this spec listed
  it as work; that was an unchecked assumption that accounts-only remote mode
  implied a login gate.

Explicitly out of scope:

- **Cloud VMs on this path.** The supported configuration is a box on a network
  you trust. The transport is internet-grade, so nothing technically prevents a
  public IP, and non-private addresses are allowed rather than blocked, but they
  are not a documented or supported configuration.
- **Connect Mobile convergence.** The existing LAN listener and its plaintext
  posture (ADR 0001) are untouched. Phones keep working exactly as they do.
  Converging the two auth paths is a later decision.
- **mDNS or Bonjour discovery.** Typing the IP is the flow.
- **NAT traversal, relays, tunnels.** ADR 0002 rejected tunnels and this does
  not reopen that.
- **Multi-user accounts on one box.** A paired box has one passcode and belongs
  to one person, who may connect from several Macs.
- **Agent CLI authentication.** Getting `claude` or `codex` logged in on the box
  is the user's job over SSH. `ao doctor` through the gateway reports whether
  it worked.

## Behavior

### On the box

The user gets an `ao` binary onto the box and runs the pair-mode setup. It runs
the same preflight, package install, and systemd unit installation that
`ao setup-vm` runs today, minus the ACME configuration and minus the device-flow
account bind. On first run it:

1. Generates a machine id if `machine.json` does not have one.
2. Generates a long-lived self-signed TLS certificate and persists it under the
   state root, so upgrades and restarts do not force a re-pair.
3. Generates an 8-character alphanumeric passcode, stores only its hash, and
   prints the plaintext exactly once alongside the certificate fingerprint and
   the box's listening address.

Re-running setup does not rotate the passcode or the certificate. A separate
rotate command regenerates the passcode, prints the new one, and drops every
connected client, matching the semantics Connect Mobile already has.

The two systemd units are the same two units a hosted VM runs: the loopback
daemon, unchanged and unauthenticated, and the gateway in front of it. The
gateway in pair mode binds only the HTTPS port. It does not bind `:80`, because
there is no ACME HTTP-01 challenge to answer.

### On the Mac

The user adds a machine by entering its address, port, and passcode. On first
connection the app shows the certificate fingerprint and the user compares it
against what the box printed. Accepting pins that fingerprint for that machine.

From then on the paired box appears in the machine picker next to the local
machine and any hosted machines, and behaves like any other machine: full board,
terminal, SSE, notifications, and `ao doctor`. An unreachable box stays in the
list marked unreachable with its last-seen time rather than disappearing.

If the fingerprint ever changes, the connection is refused outright with an
explicit re-pair action. There is no click-through and no "connect anyway". A
dismissible warning would delete the only security that trust-on-first-use
provides, so it is not offered.

Sign-in is already optional and stays that way. A user whose machines are all
local or paired never sees a login requirement. Signing in remains necessary for
hosted machines, and the picker labels each machine by origin so the two are
never confused.

### Failure modes

- Wrong passcode: the gateway returns 401 and the existing per-source lockout
  throttles repeated attempts. The lockout is per source, so a hostile device on
  the LAN cannot lock out the real Mac.
- Box unreachable: the machine stays listed as unreachable with a last-seen
  time. Reachability is probed, and a failed probe is never treated as proof the
  box is gone.
- Fingerprint mismatch: hard refusal plus a re-pair action, as above.
- Passcode rotated on the box: connected Macs drop and must re-enter the new
  passcode. The pinned certificate is unaffected, so no fingerprint re-check.

## Design and approach

**The gateway serves the paired listener, not the daemon.** ADR 0002 moved TLS
termination and certificate lifecycle out of the daemon deliberately, and the
gateway already owns the deny-by-default path allowlist that keeps `/shutdown`,
telemetry, and mobile control unreachable from off-box. Pair mode is a second
configuration of that existing process: a different certificate source
(self-signed instead of `autocert.Manager`) and a different credential check
(passcode instead of machine-audience JWT), reusing the same proxy, the same
allowlist, and the same loopback target. The daemon is not modified at all.

`mobilebridge.PasswordMatches` and the hashing already exist and are reused
rather than reimplemented. The wire shape is the one the mobile client already
speaks: `Authorization: Bearer <passcode>`. The per-source `lockout` in
`backend/internal/httpd/auth.go` is the model for the gateway's throttle.

**Pinning must cover the WebSocket, not just REST.** The terminal mux and event
streams run over the same TLS connection, so the pin has to be enforced at the
Electron session level (`setCertificateVerifyProc` in the main process) rather
than per-fetch. Pinning only the REST client would leave the terminal
unauthenticated against a swapped certificate.

**Paired machines are a third origin, not a new concept.** The renderer already
has `AoMachine`, `parseMachineOrigin`, reachability, and `formatLastSeen` in
`frontend/src/shared/ao-machines.ts`. Paired boxes slot in alongside `local` and
hosted rather than growing a parallel model. The paired-machine registry and the
pinned fingerprints live under `~/.ao/hosted`, per the state-root hard rule,
never in the control plane.

**This needs ADR 0003.** It adds a third network-facing bind configuration and
partially reopens ADR 0002's ruling that bare-IP connections are out of scope.
Without a recorded decision, AGENTS.md's hard rule makes the whole feature a
violation on sight, and the next agent to read it will correctly flag it.

### Key files

- `backend/internal/vmgateway/config.go`, `server.go`, `token.go`, `proxy.go`,
  `machinefile.go` — gateway mode, certificate source, credential check.
- `backend/internal/cli/setupvm.go` and its plan files — provisioning, distro
  gate, passcode and fingerprint output.
- `backend/internal/mobilebridge` — reused password hashing and comparison.
- `frontend/src/shared/ao-machines.ts` — paired origin.
- `frontend/src/main.ts` — certificate verification procedure for pinning.
- `frontend/src/renderer/lib/api-client.ts` — transport selection per machine.
- `frontend/scripts/build-daemon.mjs` and `.github/workflows/` — standalone
  Linux binary.

## Deferred to build time

- Exact port number for the pair-mode listener.
- Precise wording of the fingerprint comparison screen.
- Whether the rotate command is `ao pair rotate` or a flag on an existing
  command.
- Fingerprint display format (full hex versus a truncated, grouped rendering),
  as long as it is SHA-256 and comparable by eye.

## Task breakdown

### Batch 1 (no dependencies, fully parallel)

1. **Standalone Linux `ao` binary.** `build-daemon.mjs` compiles for the host
   platform only and the binary ships bundled inside the Electron app, so there
   is currently no way to get a bare `ao` onto a Linux box except building from
   source. Add cross-compiled `linux-x64` and `linux-arm64` artifacts and a
   release path for them. Hard blocker: without this the Pi story cannot ship.
2. **ADR 0003 and the AGENTS.md carve-out.** Record the pair-mode bind, its
   trust boundary, and why bare IP is now in scope for this path. Land early so
   later PRs are not flagged as hard-rule violations.
3. **Gateway pair-mode configuration and certificate.** Extend `Config` with a
   pair mode, generate and persist a long-lived self-signed certificate under
   the state root, compute and expose its SHA-256 fingerprint, and skip the
   `:80` bind and `autocert.Manager` in this mode.
4. **Withdrawn: make desktop sign-in skippable.** Audited on 2026-08-17 and
   already true on `develop`, with a regression test already guarding it. See
   the "Already true" note under Scope. Task numbers are kept as-is so the
   dependency references below stay valid.

Status: task 1 shipped in PR #88, task 2 in PR #87.

### Batch 2

5. **Gateway passcode authentication.** Replace JWT verification with passcode
   verification in pair mode, reusing `mobilebridge.PasswordMatches` and adding
   the per-source lockout. The path allowlist is unchanged, so control routes
   stay unreachable. Depends on task 3.
6. **Desktop paired-machine registry and pin store.** Persist paired machines
   and their pinned fingerprints under `~/.ao/hosted`, and wire
   `setCertificateVerifyProc` so pinning covers WebSocket and SSE, not only
   REST. Depends on task 3 for the fingerprint format.

### Batch 3

7. **Pair-mode provisioning.** Add pair mode to `ao setup-vm`, reusing the
   existing preflight, apt, tmux, and systemd machinery while skipping ACME and
   the device-flow bind. Relax the distro gate to Debian-family so Raspberry Pi
   OS passes. Generate and print the passcode and fingerprint once. Depends on
   tasks 1, 3, 5.
8. **Add-machine UI and transport wiring.** Manual entry of address, port, and
   passcode; the fingerprint comparison screen; paired machines in the picker
   with origin labels and unreachable state. Depends on tasks 5, 6.

### Batch 4

9. **End-to-end verification and documentation.** A real Debian arm64 box paired
   from a real Mac: board, terminal, SSE, `ao doctor`, passcode rotation,
   fingerprint-change refusal, and lockout under repeated bad passcodes. Update
   the README's getting-started section to present pairing as the no-domain
   path.
