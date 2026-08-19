# Seamless machine onboarding: pairing is the floor, the account is the ladder

Date: 2026-08-19
Status: Approved (design); implementation planned separately
Owner: Harshit Singh Bhandari

## Why

Adding a machine to Hosted AO today has two front doors and both have walls.
The hosted path demands a domain the user owns, DNS they control, and an
account, which is out of proportion for pointing the app at a box they can
already SSH into. The pair path (ADR 0003) removes all three walls but is
scoped to LAN boxes and buried in settings as a secondary mode. The result is
a product surface that feels clobbered even though the code underneath is
sound (two audits on 2026-08-19 found ~143 removable lines, all copy-paste
across the two parallel paths, and no structural rot).

Upstream is meanwhile building AO Cloud, a managed SaaS where agents run on
Untrivial's infrastructure behind a closed-source broker. Hosted AO's
differentiated position is the opposite architecture: your own hardware,
reached directly, with no third party required. This spec commits to that
position.

## Decision summary

1. **Pairing becomes the default way to add any machine**, including cloud
   VMs. This reverses ADR 0003's "cloud VM is not a supported configuration"
   scoping; a new ADR 0004 records the reversal and amends the AGENTS.md
   carve-out. The transport itself (self-signed cert, SHA-256 fingerprint
   pinning, 8-character passcode, per-source lockout, hard refusal on
   mismatch) is unchanged.
2. **The account/control-plane path is demoted to an optional layer** (the
   "ladder"). It keeps working untouched for existing registered machines and
   is no longer the front door. Nothing of it is deleted in v1.
3. **Upstream's AO Cloud is permanently hidden in Hosted AO builds.** Their
   cloud UI hides itself when no broker is configured (upstream commit
   bf5bcb9a8); our fork pins it unconfigured. Recorded here, implemented
   during the Phase 0 merge, reversible later.
4. **The pending upstream merge executes first** (Phase 0), before any
   onboarding work, so the seam files are reconciled once instead of twice.

## The pairing string

The single contract everything hangs on:

```
ao-pair://v1/<addr>[,<addr>...]#<sha256-fingerprint>:<passcode>
```

- `v1` is a literal version tag so the format can grow fields without
  breaking old strings. Parsers reject unknown versions explicitly.
- Each `addr` is `host:port`. Private (RFC 1918) addresses are listed before
  public ones. The list is candidate hints, not identity.
- `<sha256-fingerprint>` is the gateway certificate's SHA-256, the machine's
  identity.
- `<passcode>` is the existing 8-character alphanumeric passcode, the
  credential.

The string is treated exactly like a Postgres DSN: it is a credential.
Printed once at pair time, never logged, never written to disk in plaintext
on the box (the box keeps only the passcode hash, as today).

Supporting commands on the box:

- `ao pair` provisions (or reuses) the daemon and pair-mode gateway, then
  prints the string once. Re-running it on an already-provisioned box cannot
  reprint the full string (only the passcode hash is stored); it behaves as
  `ao pair show` and points the user at rotation if they need a new
  credential.
- `ao pair show` reprints current addresses and fingerprint. It cannot and
  does not print the passcode; the desktop already holds it, and an address
  change is the only thing `show` exists to communicate.
- Passcode rotation (existing behavior) regenerates the passcode, prints a
  full new string, and drops connected clients. The certificate pin is
  undisturbed. Certificate loss (rebuilt box, lost state root) presents as a
  fingerprint mismatch and requires a deliberate re-pair; that remains
  intended behavior, not a bug.

## Box-side flow

```
curl -fsSL get.agentlab.in | sh
```

- `get.agentlab.in` serves a static install script (static hosting only, no
  server component). The script detects OS/arch, downloads the static `ao`
  binary from GitHub Releases (linux amd64 and arm64; the
  `linux-cli-binaries.yml` workflow already builds CLI binaries), installs
  it, and runs `ao pair`.
- `ao pair` reuses the existing pair-mode provisioning that `ao setup-vm`
  grew in PR #95: daemon and gateway systemd units, certificate and passcode
  mint, state under `~/.ao/hosted`. Preflight warns about missing git/tmux/
  harness but does not block; `ao doctor` covers depth later.
- The same command is the whole onboarding for a Raspberry Pi on the desk
  and a cloud VM in a datacenter. The out-of-band trust channel is the user's
  own terminal (physical or SSH), which is exactly the channel TOFU needs.

## Desktop-side flow

"Add machine" becomes a single paste box, grown from the existing
`AddPairedMachineDialog` and promoted to the one front door. Account sign-in
moves to settings, demoted but untouched.

On paste:

1. Parse the string (version, addresses, fingerprint, passcode).
2. Race the candidate addresses concurrently with a private-address head
   start (happy-eyeballs style). An address qualifies only by presenting a
   certificate whose SHA-256 equals the pasted fingerprint, verified by the
   existing session-level pin (`setCertificateVerifyProc`, PR #91).
3. First qualifying address wins and becomes last-known-good. The passcode
   rides as the Bearer credential (existing transport, PR #96).
4. Persist the machine under `~/.ao/hosted`: name, ordered address hints,
   pinned fingerprint, passcode.

Reconnect order is always: last-known-good first, then re-race the hints.

Pin semantics have two deliberately distinct contexts:

- **Discovery** (racing candidate addresses): a wrong certificate on an
  address means "not this box", skip it silently. A stale DHCP lease owned by
  someone's printer is not an attack.
- **Steady state** (an established address suddenly presents a different
  certificate): hard refusal with an explicit re-pair action, no
  click-through, exactly ADR 0003's rule.

## Addressing model

- **Identity is the certificate fingerprint. Addresses are hints.** An IP
  change never requires re-pairing and never touches the cert or passcode.
- v1 recovery for a changed IP: edit the address in the machine's settings,
  or run `ao pair show` on the box and paste the new address.
- The ordered-hints list is deliberately shaped for post-v1 extensions, none
  of which are built now: mDNS re-discovery on the LAN (gateway announces
  `_ao-pair._tcp` with its fingerprint, which is public information; a
  spoofed announcement is useless because the certificate must still prove
  the pin), a managed DNS name, or an account-registry heartbeat. Each slots
  into the hints list without touching the trust model.

## Convergence cleanup

From the 2026-08-19 audits, folded into the same work rather than a separate
campaign (~143 lines):

Frontend:
- One shared atomic-JSON-write helper replaces three verbatim copies
  (`ao-account-store.ts`, `ao-machines.ts`, `paired-machines.ts`).
- One cached-async-token helper replaces the duplicated shape in
  `ao-control-token.ts` and `ao-machine-token.ts`.
- One gateway-cookie install/drop helper replaces the verbatim pair in
  `machine-transport.ts` and `paired-machine-transport.ts`.
- One parameterized toggle settings component replaces `CloudSection` and
  `DeveloperModeSection` (which also fixes the missing i18n in one of them).
- Settings sections collapse around the single Add machine flow.

Backend:
- Extract `resolveChownIDs` for the duplicated uid/gid preamble
  (`setupvm_bind.go`, `setupvm.go`).
- Collapse `buildSetupVMPlan`/`buildSetupVMPlanPair` into one function with a
  `pair` flag.
- Extract a `probe` helper for the five repeated timeout/exec triples in
  `doctor.go`; delete the pass-through `commandOutput` wrapper; delete the
  unreachable `VersionArg == ""` branch.

## Explicitly not in v1 (the ladder, designed for, not built)

- Managed subdomains (`m-<id>.m.agentlab.in`) with control-plane DNS and
  ACME certificates.
- Machine-list/pin sync across desktops through the account.
- Retiring the JWT/machine-token path.
- Mobile or web clients for paired machines (they need the managed-cert
  ladder first, since they cannot pin).
- mDNS auto-rediscovery (first item after v1).

The load-bearing constraint on the ladder, stated now so it survives: the
ladder never introduces a second credential model for paired machines. A
managed name and CA certificate change the trust anchor and the address,
never the passcode-as-credential.

## Phases

Each phase is separately PR-able against `develop`.

- **Phase 0: upstream merge** (230 commits as of 2026-08-19, analyzed the
  same day; base verified healthy). PR sequence per the merge playbook: safe
  bulk, mechanical, regenerate, seam clusters one at a time, migrations. New
  hosted-only migrations number >= 0100. The AO-Cloud-hidden pin and the
  addition of upstream's new `/api/v1/mobile/devices` routes to the
  gateway's `blockedAPIPrefixes` land here. The `connect-src https: wss:`
  CSP grant in `vite.renderer.config.ts` must survive; upstream's version
  silently drops it. `develop` is frozen for unrelated features during this
  phase.
- **Phase 1: the pairing string.** Box side: emit from `ao pair`, add
  `ao pair show`. Desktop side: parse, race, single Add machine flow.
- **Phase 2: the installer.** Static install script, release plumbing,
  `get.agentlab.in` hosting.
- **Phase 3: convergence cleanup.** The audit list above plus the settings
  collapse.

## Testing

Per repository conventions (AGENTS.md):

- Table-driven Go tests for `ao pair`/`ao pair show` and any gateway
  changes; usage errors exit 2, daemon/runtime failures exit 1.
- Vitest unit tests for pairing-string parse (including unknown-version
  rejection and malformed strings) and the racing/pin-context logic.
- One e2e against a locally started pair-mode gateway covering
  paste-to-board.
- Phase 0 verification follows the playbook's post-merge checks: state-root
  grep, packaged build on Node 20, real remote machine, gateway boundary
  probes.

## Security invariants (unchanged, restated because this touches them)

- The daemon stays loopback-only and unauthenticated. No new listener
  anywhere; pair mode remains a configuration of the existing gateway
  process.
- `/shutdown`, telemetry, and mobile control are never proxied, and the new
  upstream mobile-device routes join the blocklist in Phase 0.
- Fingerprint mismatch at an established address is a hard refusal with no
  click-through.
- The box stores only the passcode hash; comparison is constant-time with
  per-source lockout.
- All state stays under `~/.ao/hosted`, derived from
  `config.StateRootSegments()` / `shared/state-root.ts`.
