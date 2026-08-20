# 3. A pair mode for the VM gateway: self-signed TLS and a passcode, for a box reached by bare IP

Date: 2026-08-17
Status: Accepted, superseded in part by 0004 (cloud VM scoping)

> Note (2026-08-20): the Decision and Consequences below say a cloud VM "is
> not a documented or supported configuration" for pair mode. That scoping is
> reversed by `docs/adr/0004-pairing-string-and-cloud-pair-scope.md`, which
> makes pair mode the front door for any machine, cloud VMs included. The
> transport decided here, self-signed TLS, fingerprint pinning, the passcode
> credential, per-source lockout, and the hard refusal on a steady-state
> mismatch, is unchanged and unaffected; only the "which machines this mode
> officially covers" statement is amended. The rest of this ADR stands as the
> historical record.

## Context

`docs/adr/0002-hosted-public-gateway.md` added `ao vm serve`, the VM gateway: a
separate process from the daemon that binds `:80` and `:443`, obtains a Let's
Encrypt certificate via ACME, verifies the AO access token on every request, and
reverse-proxies to the loopback daemon. That ADR also recorded a scope
limitation: a certificate cannot be issued for a bare IP, "which is why
connecting by bare IP is explicitly out of scope for hosted AO and the user must
bring a domain they own."

What has changed since then is the kind of box people want to drive. Not a cloud
VM, but a machine they already own on a network they already trust: a Raspberry
Pi, a homelab server, a spare laptop in the next room. For that box, the two
requirements the hosted path inherits are both walls. A domain with DNS pointed
at the box is a wall because the box has a private address and no public name,
and buying and delegating a domain to reach a Pi on your own LAN is out of
proportion to the task. An account on the ao.agentlab.in control plane is a wall
because nothing about running an agent on hardware you own on a network you own
needs a third party to be involved, and the machine-audience token model in 0002
requires the control plane to be reachable to hand out and re-sign tokens.

The technical fact in 0002 still holds: ACME cannot issue for a bare IP. But the
domain requirement was never a requirement of TLS, it was a requirement of using
a public certificate authority as the trust anchor, because a CA can only attest
to a name it can validate. If the trust anchor is something else, the domain
requirement disappears with it, and the transport stays exactly as strong.

Something else is available on this path that was not available on the hosted
path. The user of a paired box has the box in front of them, or an SSH session
on it, at setup time. That gives an out-of-band channel the hosted flow could not
assume: the box can print a certificate fingerprint that the user carries to the
desktop app by eye. Trust-on-first-use is only sound when such a channel exists,
and here it does.

## Decision

Add a **pair mode** to `ao vm serve`. It is a second configuration of the gateway
process introduced in 0002, not a new process and not a new listener in the
daemon:

- **Self-signed TLS, no ACME.** The box generates a long-lived self-signed
  certificate on first run and persists it under the state root, so restarts and
  upgrades do not force a re-pair. Pair mode binds only the HTTPS port. It does
  not bind `:80`, because there is no HTTP-01 challenge to answer.
- **Trust-on-first-use certificate pinning replaces the certificate authority.**
  Setup prints the certificate's SHA-256 fingerprint once, next to the box's
  listening address. On first connection the desktop app shows the fingerprint it
  received and the user compares it against what the box printed; accepting pins
  that fingerprint to that machine. This is the substitution that removes the
  domain requirement: the trust anchor becomes a fingerprint the user carried out
  of band, and a fingerprint does not care whether the peer is named by a domain
  or by an IP address.
- **Fingerprint mismatch is a hard refusal.** The connection is rejected outright
  with an explicit re-pair action. There is no click-through and no "connect
  anyway", because a dismissible warning would delete the only security that
  trust-on-first-use provides.
- **Passcode instead of a JWT.** The box generates an 8-character alphanumeric
  passcode, stores only its hash, and prints the plaintext exactly once. The
  gateway compares it constant-time on every request (`Authorization: Bearer
  <passcode>`, the wire shape the mobile client already speaks, reusing
  `mobilebridge.PasswordMatches`) and applies the same per-source lockout the
  daemon's LAN listener uses. A separate rotate command regenerates the passcode
  and drops every connected client, matching Connect Mobile's semantics.
- **No control plane at all.** Pair mode never contacts ao.agentlab.in: no
  device-code flow, no account bind, no token issuance, no telemetry to it. The
  paired-machine registry and the pinned fingerprints live under `~/.ao/hosted`
  on the Mac, per the state-root hard rule. Desktop sign-in becomes optional, so
  a user whose machines are all paired never needs an account.
- **Everything else in 0002 is unchanged.** Same process, same reverse proxy,
  same deny-by-default path allowlist. `/shutdown`, telemetry, and mobile
  control are still never proxied and remain reachable only from the box itself.
- **The daemon is not modified.** It stays bound to `127.0.0.1` and
  unauthenticated, per the existing AGENTS.md hard rule, and has no awareness
  that a gateway is in front of it. ADR 0001's opt-in plaintext LAN listener is
  untouched; phones keep working exactly as they do today, and converging the two
  credential paths is a later decision, not this one.

This **narrows the bare-IP scope statement in 0002** rather than reversing it.
The hosted path is unchanged: a hosted machine still requires a domain the user
owns and an account, and still gets ACME plus machine-audience JWT verification.
Bare IP is in scope only for pair mode, whose supported configuration is a box on
a network the user trusts. The transport is internet-grade, so nothing technically
prevents a public address, and non-private addresses are allowed rather than
blocked, but a cloud VM on this path is not a documented or supported
configuration. A pointer to this ADR has been added to 0002; its decision text is
left as the historical record.

As in 0002, this is scoped to `ao vm serve` in pair mode specifically, not to
"the `ao` binary may bind non-loopback ports" in general, and local mode never
runs the gateway in either configuration. AGENTS.md is amended with exactly that
narrow carve-out, mirroring how 0001 scoped the loopback-only rule and how 0002
carved out the hosted gateway.

### Alternatives considered and rejected

- **Plaintext on the LAN, in the style of ADR 0001.** The scope here is a trusted
  network, and 0001 already accepted plaintext for exactly that setting, so
  reusing the posture would have been the smaller change. Rejected because the
  transport should be sound even when the scope is a network you trust: in
  plaintext the passcode and every terminal byte are readable by anything on the
  segment, and "home network" is not a security boundary anyone can actually
  verify. The reason 0001 deferred TLS does not apply here either. That decision
  turned on the client being Expo/React Native, where pinning a self-signed
  certificate would have needed native modules across three transports. The
  client here is the Electron desktop app, where one certificate verification
  procedure in the main process covers REST, the `/mux` WebSocket, and SSE
  together. With the cost of pinning that much lower, inheriting 0001's
  conclusion would be inheriting it without its premise.
- **Magic DNS plus Let's Encrypt (the sslip.io style).** A wildcard resolver that
  turns an IP address into a resolvable hostname would let ACME issue a real
  certificate for a box on a private address, keeping one certificate story for
  both modes. Rejected because it puts a third party in the availability path of
  a purely local connection: if that resolver is down, rate-limiting, or simply
  gone in two years, pairing and every subsequent renewal break, and the box
  needs working outbound internet to serve a Mac sitting next to it. It also
  shares certificate-authority rate limits with everyone else using the same
  service, so someone else's usage can exhaust issuance for our users, and a
  failure of that kind is both hard to diagnose and entirely outside this
  project's control. Trading a domain requirement for a dependency on a free
  public resolver is not removing the wall, it is moving it somewhere we cannot
  see it.
- **Extend the daemon's own LAN listener instead of the gateway.** The daemon
  already has an opt-in LAN listener with bearer-password auth and a per-source
  lockout (0001), so adding TLS to it looks like the shortest path to the same
  feature. Rejected because 0002 moved TLS termination and certificate lifecycle
  out of the daemon deliberately, and this would move them straight back in:
  certificate generation, persistence, and rotation would live inside the process
  the hard rule keeps loopback-only and unauthenticated, which is the one process
  whose network posture we most want to be able to reason about without
  qualification. The gateway also already owns the deny-by-default path allowlist
  that keeps `/shutdown`, telemetry, and mobile control unreachable from off-box,
  so building the paired listener in the daemon would mean reimplementing that
  allowlist in a second place, where the two copies can drift.

## Consequences

- **The gateway now has two credential models.** Hosted mode verifies a
  machine-audience AO access token against the control plane's keys; pair mode
  compares a locally generated passcode against a stored hash. The mode is fixed
  at configuration time and a request is never checked against both, so neither
  model can be used as a fallback for a failure of the other. Both now have to be
  audited, and a change that widens one must be checked against the other.
- **Two certificate sources in one process.** `autocert.Manager` for hosted mode,
  a persisted self-signed certificate for pair mode. Pair mode's skipped `:80`
  bind means the ACME path and the pair path diverge in what they listen on as
  well as what they present, so a change to bind setup has to be reasoned about
  in both modes.
- **Pinning is enforced at the Electron session level**, via
  `setCertificateVerifyProc` in the main process, not per fetch. The terminal mux
  and the event streams ride the same TLS connection as REST, so a pin applied
  only in the REST client would leave the terminal and SSE trusting a swapped
  certificate. The consequence is that the desktop main process is now
  security-relevant for remote transport, and the pin store under `~/.ao/hosted`
  is security-relevant state rather than a cache.
- **Bare IP is now a supported way to reach an AO box**, on this path only.
  0002's scope statement reads, from here on, as scoped to the hosted path.
- **A paired box needs no account**, which makes sign-in optional in the desktop
  app for the first time. The machine picker labels each machine by origin so a
  paired box and a hosted VM are never confused, and hosted machines still
  require sign-in.
- **Losing the printed passcode is recoverable, losing the box's certificate is
  not.** Rotation on the box issues a new passcode without disturbing the pin;
  replacing the certificate (a rebuild, or a restored state root that lost it)
  presents as a fingerprint mismatch and requires a deliberate re-pair, which is
  the intended behavior and not a bug to be smoothed over.
- **AGENTS.md's network-bind hard rule needs the pair-mode carve-out** (added in
  this change), or future agents will correctly flag the pair-mode listener as a
  violation, exactly as 0001 and 0002 each needed their own.
