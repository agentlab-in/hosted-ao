# 4. The pairing string, and pairing as the front door for any machine, cloud VMs included

Date: 2026-08-20
Status: Accepted

## Context

`docs/adr/0003-pair-mode-gateway.md` built pair mode's transport (self-signed
TLS, a trust-on-first-use SHA-256 fingerprint pin, an 8-character passcode)
for a box the user already has in front of them: a Raspberry Pi, a homelab
server, a spare laptop. That ADR's Decision and Consequences went further than
the transport, though, and scoped the *configuration* too: "a cloud VM on this
path is not a documented or supported configuration," with the hosted path
(`docs/adr/0002-hosted-public-gateway.md`, a domain the user owns plus a
control-plane account) staying the only route to a machine not on a trusted
network.

`docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md`
("Seamless machine onboarding: pairing is the floor, the account is the
ladder") reconsiders that scoping, not the transport. Adding a machine today
has two front doors and both have walls: the hosted path demands a domain, DNS
the user controls, and an account, which is out of proportion for pointing the
app at a box the user can already SSH into; the pair path removes all three
walls but is buried behind the LAN-only scoping above. Two audits on
2026-08-19 confirmed the code underneath both paths is sound (~143 removable
duplicate lines, no structural rot), so the walls are a scoping and product
decision, not a technical debt.

The scoping in 0003 was never a requirement of the transport. Trust-on-first-use
pinning needs an out-of-band channel to carry the fingerprint, and the channel
0003 relies on, the user's own terminal on the box (physical or SSH), exists
identically for a homelab Pi and a cloud VM. Nothing in the pinning story
depends on the bytes travelling over a network the user trusts; the fingerprint
comparison is exactly as sound over the public internet as on a LAN. "Bare IP
is in scope only for pair mode" (0003) narrowed 0002's domain requirement
correctly; "a cloud VM is not a supported configuration" was a separate,
looser claim that this ADR reverses.

## Decision

**Pairing becomes the default way to add any machine, LAN box or cloud VM,
reached by bare IP or by domain.** This reverses 0003's cloud-VM scoping
statement; the transport it built (self-signed cert, SHA-256 fingerprint
pinning, 8-character passcode, per-source lockout, hard refusal on a
steady-state mismatch) is unchanged and is exactly what a cloud VM now uses
too. The hosted/account path (0002) is not deleted: it is demoted to an
optional layer for machine-list sync and, later, managed subdomains, and it
keeps working untouched for machines already registered through it.

**The pairing string is pair mode's single front door:**

```
ao-pair://v1/<addr>[,<addr>...]#<sha256-fingerprint>:<passcode>
```

- `v1` is a literal version tag so the grammar can grow without breaking old
  strings; a parser rejects an unrecognized version rather than guessing.
- Each `addr` is `host:port`. Private (RFC 1918) addresses are listed before
  public ones. The list is candidate hints, not identity.
- `<sha256-fingerprint>` is the gateway certificate's SHA-256: the machine's
  identity.
- `<passcode>` is the existing 8-character alphanumeric passcode: the
  credential.

The string is treated exactly like a Postgres DSN: printed once by `ao pair`
(re-addressable, without the passcode, via `ao pair show`), never logged,
never written to disk in plaintext on the box, which keeps only the passcode
hash as it did before this ADR. Pasting it into the desktop app's Add Machine
dialog is the whole onboarding step on the desktop side.

**Identity is the certificate fingerprint; every address is a hint, raced.**
The desktop races every address the string lists concurrently, a private
address given a head start, and pins whichever one first presents a
certificate matching the pasted fingerprint. That address becomes
last-known-good; an IP change never requires re-pairing, since nothing about
the pin is address-derived. A wrong certificate during this race means "not
this box, try the next candidate," silently; only a certificate change at an
address the user already trusts (steady state, post-pin) is the hard refusal
0003 already specified. This two-context split is deliberate: discovery noise
(a stale DHCP lease, a printer answering on the wrong port) is not an attack,
and conflating it with a steady-state mismatch would either make pairing
flaky or make the hard refusal too noisy to trust.

## Alternatives considered and rejected

- **Keep the account path as the only route to a cloud VM, and offer pairing
  for LAN boxes only.** This is the status quo 0003 left in place, and it is
  the problem this ADR exists to fix: two front doors for a trust story that
  the box already tells identically either way, with an account and DNS wall
  in front of the one that happens to run somewhere else. Nothing about a
  cloud VM makes the fingerprint TOFU story weaker; it only changes where the
  bytes travel.
- **Ship the pairing string without racing, requiring the user to pick the
  right address from the list themselves.** Rejected because the string
  already carries every address the box knows about itself (LAN-private and
  public), and asking the user to pick defeats the "one paste" goal the string
  exists for. Racing costs nothing extra to build once the parser and the
  probe call already exist, and a private-address head start keeps the common
  case (a box on the same network) fast without a round trip to a public
  address first.
- **Defer the address list to mDNS discovery and print only the fingerprint
  and passcode.** Rejected for the reason 0003 rejected magic DNS for
  certificates: it would put the whole pairing flow's availability behind a
  discovery mechanism not built yet. mDNS re-announce/browse is explicitly
  future work (see the spec's "Addressing model"); the ordered-hints list in
  the string already solves "how does the desktop reach it" today, and mDNS
  slots in later as one more hint source without touching the trust model.

## Consequences

- **Pair mode is now security-relevant for the general internet, not just a
  home LAN**, but the transport's assumptions do not change to accommodate
  that: 0003 already terminated TLS with a self-signed cert pinned at the
  Electron session level and treated the network itself as untrusted. Nothing
  here weakens that; it only removes a configuration restriction that the
  transport never needed.
- **`docs/adr/0003-pair-mode-gateway.md`'s cloud-VM scoping is now historical.**
  Its Decision and Consequences sections describing that scoping are left as
  written for the record; this ADR is the amendment, and 0003 carries a
  cross-reference note to it, mirroring how 0002 carries a note pointing to
  0003.
- **The pairing string is a new parsed format with two independent
  implementations** (Go in `backend/internal/pairstring`, TypeScript in
  `frontend/src/shared/pair-string.ts`) that must never drift; both are tested
  against the same golden vectors so a grammar change is a vector change
  first, an implementation change second.
- **AGENTS.md's pair-mode carve-out sentence is checked against this ADR** and
  amended if its wording still reads as LAN-scoped, the same touch 0001, 0002,
  and 0003 each needed for their own network-bind change.
- **The installer (`get.agentlab.in`, static hosting, downloading the released
  `ao` binary and running `ao pair`) and mDNS re-discovery are explicitly not
  built by this ADR.** The installer is Phase 2 of the onboarding spec; until
  it ships, reaching a machine still means running `ao pair` after getting the
  `ao` binary onto it some other way, and the desktop-side paste flow this ADR
  describes is unaffected by which path put the binary there.
