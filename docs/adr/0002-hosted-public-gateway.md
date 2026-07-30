# 2. A public TLS gateway on a user-owned VM, as a separate process from the daemon

Date: 2026-07-30
Status: Accepted

## Context

AGENTS.md carries a hard rule: the daemon's primary listener stays bound to
`127.0.0.1` and unauthenticated. That rule holds because the OS guarantees
nothing off-box can reach a loopback-only socket, so the daemon never needs
its own auth layer.

Hosted AO lets a user run coding agents on a cloud VM they provision and pay
for, driven from the desktop app on their laptop. That VM has to be reachable
over the public internet: the laptop is not on the same network as the VM, and
there is no LAN to fall back on the way the mobile "Connect Mobile" feature
does (`docs/adr/0001-lan-listener-for-mobile.md`). TLS is not optional here.
Unlike the LAN Listener, this traffic crosses the open internet, so plaintext
is not an acceptable posture. TLS in turn requires binding `:443`, and
obtaining a certificate via ACME (Let's Encrypt) requires answering the
HTTP-01 challenge on `:80`. TLS also requires a hostname: a certificate cannot
be issued for a bare IP, which is why connecting by bare IP is explicitly out
of scope for hosted AO and the user must bring a domain they own.

Binding `:80` and `:443` on the VM is therefore unavoidable for some part of
the system. The question this ADR answers is whether that requirement can be
satisfied without weakening the daemon's own loopback guarantee.

## Decision

Add `ao vm serve`, a Cobra command in the existing `ao` binary
(`backend/internal/cli/*.go` conventions), started by its own systemd unit as
a process **separate from the daemon**. This is the VM gateway.

The gateway, not the daemon:

- Binds `:80` and `:443` on the VM.
- Obtains and renews a Let's Encrypt certificate for the configured domain via
  ACME.
- Verifies the AO access token (signature, `iss`, `aud` pinned to this
  machine's id, `exp`, and `sub` against the machine's single allowlisted
  account) on every request, and rejects unauthenticated or invalid requests
  with 401 before they reach the daemon.
- Answers CORS preflights itself, without requiring a token; every actual
  request still requires one.
- Reverse-proxies authenticated requests to the loopback daemon.
- Never proxies the loopback-only control routes: `/shutdown`, telemetry, and
  mobile control. Those remain reachable only from the box itself, exactly as
  today.

The daemon itself is unchanged: still bound to `127.0.0.1`, still
unauthenticated, per the existing AGENTS.md hard rule. It has no awareness
that a gateway exists in front of it. All new attack surface, TLS handling,
and token verification lives in the gateway process, not the daemon.

This is scoped to `ao vm serve` specifically, not to "the `ao` binary may bind
non-loopback ports" in general. AGENTS.md is amended (see below) to carve out
exactly this exception, mirroring how ADR 0001 scoped the loopback-only rule
to the daemon's primary listener rather than removing it.

### Blast radius

This applies only to a machine the user has explicitly provisioned, paid for,
and bound to their AO account through the `ao setup-vm` device-code flow. It
does not apply to, and does not change anything about, a developer's local
laptop: local mode never runs the gateway, never requires an account, and the
daemon there is exactly as loopback-only as it is today. A developer running
`ao start` locally sees no behavior change from this decision.

### Alternatives considered and rejected

- **Keep Caddy on the user VM as a separate TLS terminator.** The control
  plane already runs behind Caddy as a second site on the existing Azure VM,
  and Caddy remains there. But shipping a second, independently-configured
  reverse proxy to every user VM means the user (or `ao setup-vm`) has to
  install, configure, and keep patched a piece of software outside the `ao`
  binary, with its own auth story bolted on since Caddy does not know about
  AO tokens. That is more moving parts for `ao setup-vm` to install and more
  surface for the token-verification logic to live outside the codebase this
  repo owns. Caddy is removed from user VMs for this reason; it stays only on
  the control-plane box.
- **Tunnel instead of a public bind** (for example a reverse SSH tunnel or a
  managed tunneling service back to the control plane). This would avoid
  opening `:80`/`:443` on the VM at all, but it adds a persistent dependency
  on a tunnel broker being reachable and healthy for the VM to be reachable,
  contradicts the "user owns and pays for this VM, on their own domain" model
  the spec commits to, and does not remove the need for TLS termination
  somewhere in the path, it only relocates it. A direct public bind on a
  domain the user controls is simpler to reason about and matches how a
  self-hosted CI runner or any other internet-facing service the user already
  operates would be set up.

## Consequences

- The `ao` binary now has two distinct network-facing modes: the daemon's
  always-loopback, always-unauthenticated listener, and the gateway's
  always-public, always-authenticated listener. They must never be started as
  the same process or share a bind, so a future change that merges them back
  together would need its own ADR.
- The gateway is the only place token verification, ACME renewal, and public
  TLS termination need to be implemented and audited. The daemon's request
  handling, storage, and lifecycle code require no changes to support remote
  access.
- Because the gateway is systemd-managed separately, it can be restarted,
  updated, or its certificate renewed without touching the daemon process or
  interrupting in-flight agent sessions on the daemon side.
- AGENTS.md's loopback-only hard rule now needs an explicit, narrow carve-out
  for `ao vm serve`, or future agents will (correctly) flag the gateway's
  `:80`/`:443` bind as a violation.
