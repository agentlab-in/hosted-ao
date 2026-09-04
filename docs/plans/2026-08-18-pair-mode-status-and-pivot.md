# Pair mode: what shipped, what the live test found, and the pivot

Written 2026-08-18, at the point where the TLS/pinning design was abandoned in
favour of Connect Mobile's model. Supersedes the transport half of
`2026-08-16-pair-by-ip-headless-boxes.md`. Read this before touching pair mode.

## The goal, unchanged

Run the AO daemon headlessly on a box the user owns (Raspberry Pi, homelab
server, spare machine on their LAN) and drive it from the Mac app, with **no
domain and no control-plane account**. Type an address and a passcode, done.

## What shipped on `develop`

Nine PRs, all merged, all CI-green:

| PR | What |
|---|---|
| #88 | Standalone Linux `ao` binaries, x64 and arm64, cross-compiled in CI |
| #87 | ADR 0003 and the narrow AGENTS.md hard-rule carve-out |
| #89 | Gateway pair mode: config, persisted self-signed cert, `PairFingerprint` |
| #92 | Gateway passcode auth: constant-time compare, per-source lockout |
| #91 | Paired-machine registry, encrypted passcode, `setCertificateVerifyProc` pinning |
| #93 | Pairing UI: address/port/passcode entry, fingerprint comparison, mismatch refusal |
| #95 | `ao setup-vm --pair`, Debian-family distro gate, `ao vm rotate-passcode` |
| #96 | Passcode transport for REST, SSE, and the `/mux` terminal socket |
| #90, #94 | `AGENTS.md` branch-from-`develop` fix; the transport gap recorded |

## What the live end-to-end test proved

Run against a real Debian 12 box (GCP, now deleted). Verified from the Mac:

- Provisioning completes and prints a passcode and fingerprint exactly once.
- No credential gives 401, wrong passcode gives 401, correct passcode gives 200.
- `/shutdown` with a **valid** passcode gives 404: the allowlist holds, control
  routes are never proxied.
- The certificate fingerprint presented over the wire matches the printed one
  byte for byte.

The gateway, the passcode auth, the lockout, and the allowlist all work.

## What the live test found, that no unit test caught

1. **Fresh boxes are dead on arrival.** `MkdirAll` creates `~/.ao` and
   `~/.ao/hosted/vm-gateway` as root under `sudo`, and `chownSetupTree` only
   runs on the leaf directories. Both stay `root:root 0700`, so the daemon and
   gateway units both fail with `status=200/CHDIR`, unable to enter their own
   working directory. **Still unfixed in code.**
2. **The printed address is the box's internal IP.** `discoverPairListenAddresses`
   lists non-loopback interface addresses, which on any cloud VM is the private
   NIC. Correct on a LAN, useless anywhere else. **Still unfixed.**
3. **The pair certificate has no IP SAN.** Its only SAN is
   `DNS:ao-pair-gateway`, on a feature whose entire premise is connecting by
   bare IP. Latent while an accepted pin overrides name checks, but wrong.
4. **Pinning rejected every connection after pairing.** The stored pin was
   correct (verified against the live cert), and the fingerprint the verify proc
   would compute was confirmed identical. The reject came from our own
   `CERT_VERIFY_REJECT` (`-2`, matching the observed `net_error -2`), meaning
   the pin lookup missed in memory while being right on disk. The in-memory
   `pinnedByHost` index is the suspect; `pendingHosts` shadowing a pinned host
   is the specific thing to look at. **Never diagnosed to root cause**, because
   the design was abandoned first.
5. **`ao vm serve` has no `--pair` flag.** Pair mode is reachable only via the
   `AO_VM_PAIR` env var, which is how the systemd unit sets it. Its help text
   still describes hosted mode only.

## The pivot, decided 2026-08-18

**Do what Connect Mobile does: plaintext HTTP on a trusted LAN, passcode auth,
no TLS and no certificates at all.**

The phone has always worked this way. `packages/mobile/lib/config.ts` defaults
`secure: false` and `httpBase()` returns `http://`. There is no certificate, so
there is nothing to validate, pin, compare, or get wrong. ADR 0001 made that
trade deliberately: home-network-only in exchange for zero cert machinery.

Pair mode took the harder path and paid for it: a self-signed cert, a
fingerprint format three components had to agree on, session-level pin
enforcement, a comparison screen, a mismatch refusal state, and a bug that ate
the live test. None of it was required by the scope that was actually chosen,
which was LAN boxes only.

**The decision: reuse the daemon's existing LAN listener, the one the phone
already talks to.** It exists, it is shipped, it is tested, it already has
password auth (`mobilebridge.PasswordMatches`), a per-source lockout, and it
already refuses the loopback-gated control routes.

### The trade being accepted, stated plainly

Plaintext means the passcode, the source code, the agent output, and any tokens
in it travel in the clear. On a LAN the user trusts, that is ADR 0001's posture
and it is defensible. **It is not acceptable over the internet**, so a public
cloud VM is no longer a valid test target, and pair-by-IP must not be presented
as a way to reach a box across the internet.

## What to do now

### Delete

- `backend/internal/vmgateway/paircert.go` and its tests: the self-signed cert
  and `PairFingerprint`.
- Gateway pair mode in `config.go` / `server.go`, and `NewPairHandler` plus
  `requirePasscode` in `proxy.go` / `passcode.go`, **if** the gateway plays no
  part in the new design. Confirm before deleting: `ao setup-vm --pair` and the
  systemd units in #95 currently target the gateway.
- `frontend/src/main/paired-machine-cert.ts` and the
  `setCertificateVerifyProc` wiring in `main.ts`.
- The fingerprint comparison step and mismatch-refusal state in
  `AddPairedMachineDialog.tsx`, plus their `pairing.*` i18n keys in all 8
  locales.

### Keep

- #88's Linux binaries. Unaffected and still the only way to get `ao` onto a box.
- The paired-machine registry and encrypted passcode storage in
  `paired-machines.ts`, minus everything pin-related.
- The add-machine form: address, port, passcode entry.
- #95's provisioning skeleton: preflight, apt, Debian-family gate, systemd
  units, idempotency. Retarget it at the LAN listener.

### Build

- **A CLI to enable the LAN listener on a headless box and print the password.**
  This is the one genuinely new piece. Today the bridge is enabled from the
  desktop UI and the password is read off the screen; a headless box has no
  screen. The control endpoints (`status`/`enable`/`disable`/`regenerate`) are
  loopback-only on the daemon, so the command runs on the box and talks to its
  own loopback API. It must print host, port, and password once.
- **Point the desktop transport at the LAN listener** over `http://`, sending
  the password as `Authorization: Bearer <password>`, which is exactly the wire
  shape `packages/mobile/lib/config.ts` already uses.

### Rewrite

- **ADR 0003.** It records a decision that no longer holds. Supersede it with a
  new ADR recording that pair-by-IP reuses ADR 0001's LAN listener and its
  plaintext, home-network-only posture, and say why the TLS design was dropped.
- **The AGENTS.md carve-out** from #87, which describes a pair-mode gateway bind
  that will no longer exist.

### Fix regardless of design

The `sudo` ownership bug (finding 1) is not specific to the gateway. Any
provisioning path that creates `~/.ao` as root and only chowns leaf directories
leaves a box that cannot start. Fix it wherever the new design creates state.

## Test it on a LAN box, not a cloud VM

The correct target is now a Raspberry Pi or spare machine on the same network as
the Mac. The GCP box used for the first round has been deleted, and a public
cloud box would send the passcode across the internet in clear text.
