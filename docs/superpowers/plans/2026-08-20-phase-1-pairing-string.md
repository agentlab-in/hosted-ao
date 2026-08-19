# Phase 1: The Pairing String Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One command on any box prints one `ao-pair://` string; one paste in the desktop app adds the machine. No domain, no account, no fingerprint squinting.

**Architecture:** A tiny string codec implemented twice against one shared golden-vector file (Go emits, TypeScript parses). The Go side wires emission into the existing pair-mode provisioning and a new `ao pair show`. The desktop side adds a paste-first step to the existing `AddPairedMachineDialog` that parses, races candidate addresses through the existing fingerprint probe, auto-pins on match (the pasted string is the out-of-band channel), and stores address hints. All transport, cert, passcode, and pinning machinery already exists (ADR 0003, PRs #91-#96); this phase only builds the codec, the emission, and the paste flow.

**Tech Stack:** Go (Cobra CLI, table tests), TypeScript (Electron main + React renderer, Vitest), one shared JSON vector file.

**Spec:** `docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md` (sections: The pairing string, Box-side flow, Desktop-side flow, Addressing model).

## Global Constraints

- The daemon stays loopback-only and unauthenticated; nothing here touches it.
- No new network-facing bind; the gateway in pair mode is unchanged.
- All state under `~/.ao/hosted` via `config.StateRootSegments()` / `shared/state-root.ts`.
- The pairing string is a credential (Postgres-DSN treatment): never logged, never written to disk in plaintext by either side; the box keeps only the passcode hash, as today.
- Fingerprint mismatch semantics: during discovery (racing candidates) a wrong cert means skip the address silently; at an established address it stays the hard refusal already implemented.
- The passcode remains the only credential; the string never carries anything else secret.
- Branch from `develop` (after PR #98 lands), PR back to `develop`. Conventional commits, no AI attribution footer, no em or en dashes anywhere.
- Any new hosted-only SQLite migration numbers >= 0200 (none is expected in this phase).
- i18n: every new user-facing string gets a key in `en.json` plus faithful translations in the other 7 locales, matching neighboring fork keys' style.

## The grammar (single source of truth)

```
ao-pair://v1/<addr>[,<addr>...]#<fp>:<passcode>

addr      = host ":" port         (host = IPv4 | "[" IPv6 "]" | DNS name)
fp        = 64 lowercase hex chars (SHA-256 of the gateway cert, no separators)
passcode  = 8 chars [A-Za-z0-9]
```

Parsers reject: unknown version segment, zero addresses, an addr without an explicit port, port outside 1-65535, fp not exactly 64 lowercase hex, passcode not exactly 8 alphanumerics, any username/query/extra fragment. Private (RFC 1918 + loopback excluded entirely) addresses are ordered before public by the EMITTER; the parser preserves order as given. The desktop normalizes `fp` to whatever format the pin store uses (Electron's `fingerprint256` is colon-separated uppercase; the codec converts).

---

### Task 1: Golden vectors + Go codec package

**Files:**
- Create: `backend/internal/pairstring/pairstring.go`
- Create: `backend/internal/pairstring/pairstring_test.go`
- Create: `backend/internal/pairstring/vectors.json` (canonical; the TS test imports this same file by relative path)

**Interfaces:**
- Produces: `pairstring.Build(addrs []string, fpHex string, passcode string) (string, error)` (validates all inputs against the grammar; addrs are `host:port` strings already ordered by the caller) and `pairstring.Fingerprint(cert *x509.Certificate) string` (returns the 64-char lowercase hex SHA-256 of `cert.Raw`).
- Produces: `vectors.json` with shape `{"valid": [{"name", "addrs", "fp", "passcode", "string"}], "invalid": [{"name", "string", "reason"}]}`.

- [ ] **Step 1: Write `vectors.json`** with at least these cases. Valid: single IPv4 (`ao-pair://v1/192.168.1.40:8443#<64 hex>:XK4M2P7Q`), multi-address private-then-public (`192.168.1.40:8443,203.0.113.7:8443`), IPv6 in brackets (`[fd00::7]:8443`), DNS name host. Invalid: `v2` version, no addresses, addr missing port, port `70000`, fp of 63 chars, fp with uppercase, fp with colons, 7-char passcode, passcode with `-`, trailing query `?x=1`, second `#`. Use a fixed test fp (`"ab"` repeated 32 times) and passcode `XK4M2P7Q` throughout so vectors are stable.
- [ ] **Step 2: Write the failing Go test**: table test iterating `vectors.json`: every valid case's inputs must `Build` to exactly `string`; every invalid case must fail to round-trip (implement a small unexported `parse` in the test's package solely to assert invalids are rejected symmetrically, OR assert `Build` rejects the inputs where the invalid is expressible as inputs; unknown-version and structural invalids are asserted through the exported `Validate(s string) error` which Task 1 also ships for `ao pair show --check` style reuse).
- [ ] **Step 3: Run to verify failure** (`cd backend && go test ./internal/pairstring/`), **Step 4: implement** `Build`, `Validate`, `Fingerprint`, **Step 5: run to green**, **Step 6: commit** `feat(pairstring): pairing string codec with golden vectors`.

### Task 2: `ao pair show` and string emission at mint time

**Files:**
- Create: `backend/internal/cli/pair.go`, `backend/internal/cli/pair_test.go`
- Modify: the pair-mode provisioning output in `backend/internal/cli/setupvm.go` (where the fingerprint and passcode are printed today) and the passcode-rotation command's output path (`ao vm rotate-passcode`).

**Interfaces:**
- Consumes: `pairstring.Build`, `pairstring.Fingerprint`, `pairstring.Validate`; the persisted pair cert and gateway port from the vmgateway state helpers (`backend/internal/vmgateway/paircert.go`, `config.go`; read them, do not duplicate path logic); `setupVMPublicIPEndpoints` for the public-address probe.
- Produces: `ao pair show` prints `Addresses`, `Fingerprint`, and a passcode-less note (`run 'ao vm rotate-passcode' for a fresh pairing string`); provisioning and rotation print the FULL string exactly once, on its own line, prefixed `Paste this in Hosted AO:`.

- [ ] **Step 1: Write failing table tests** in the style of `backend/internal/cli/*_test.go`: (a) `ao pair show` with a stubbed cert+state dir prints the fingerprint and enumerated addresses, exits 0; (b) `ao pair show` with no pair cert exits 1 with a message pointing at provisioning; (c) address enumeration ordering: RFC 1918 interface addrs before the stubbed public-probe result, loopback excluded, gateway port appended to every addr; (d) rotation output contains a string that `pairstring.Validate` accepts.
- [ ] **Step 2: Run to verify failure. Step 3: Implement**: `ao pair` parent command with `show` subcommand (usage errors exit 2); enumeration via `net.Interfaces()` filtered to global unicast, private-first ordering, public probe reusing the existing endpoints with a short timeout and silent skip on failure. **Step 4: green. Step 5: commit** `feat(cli): ao pair show and pairing-string emission`.

### Task 3: TypeScript parser (shared module)

**Files:**
- Create: `frontend/src/shared/pair-string.ts`, `frontend/src/shared/pair-string.test.ts`

**Interfaces:**
- Produces: `parsePairString(raw: string): { addrs: {host: string, port: number}[], fingerprintHex: string, passcode: string } | { error: string }` and `toPinnedFingerprintFormat(fpHex: string): string` (converts to the exact format `paired-machines.ts` stores from `probeFingerprint`; the implementer verifies that format from `paired-machine-cert.ts` and pins it with a test).
- No Electron imports; pure module usable from renderer and main.

- [ ] **Step 1: Write the failing Vitest** importing `backend/internal/pairstring/vectors.json` by relative path: every valid vector parses to its inputs; every invalid vector returns `{error}` mentioning the reason category; plus a normalization test for `toPinnedFingerprintFormat`.
- [ ] **Step 2: fail, Step 3: implement (manual parse, no URL()), Step 4: green, Step 5: commit** `feat(desktop): pairing string parser against the shared vectors`.

### Task 4: Address hints in the paired-machine store

**Files:**
- Modify: `frontend/src/main/paired-machines.ts` (+ its test file)

**Interfaces:**
- Consumes: existing `PairedMachineRecord` (schema version 1: single `address`).
- Produces: schema version 2: `addresses: string[]` (ordered hints, `host:port` strings, first entry = last-known-good) replacing reliance on the single `address`/`port` pair, which remain as the current/winning address for compatibility with `pairedMachineId` and existing consumers. `load()` migrates v1 records (`addresses = [address:port]`). New `promoteAddress(id, addr)` moves a hint to the front on successful connect.

- [ ] **Step 1: failing tests**: v1 file on disk loads as v2 with migrated hints; `promoteAddress` reorders and persists; `add` with multiple hints persists them in order. **Step 2: fail, Step 3: implement (bump SCHEMA_VERSION to 2), Step 4: green, Step 5: commit** `feat(desktop): ordered address hints for paired machines`.

### Task 5: Paste-first Add machine flow with candidate racing

**Files:**
- Modify: `frontend/src/renderer/components/settings/AddPairedMachineDialog.tsx` (+ test), `frontend/src/main/paired-machines.ts` bridge surface + `preload.ts` + `frontend/src/renderer/lib/bridge.ts` if the race runs in main (see step 1 decision), i18n `en.json` + 7 locales.

**Interfaces:**
- Consumes: `parsePairString`, `toPinnedFingerprintFormat`, `probeFingerprint(address, port)` bridge, Task 4's hints.
- Produces: the dialog's first step is one textarea ("Paste a pairing string") with a "enter details manually" escape hatch to the existing form. On paste: parse; race all addrs via `probeFingerprint` concurrently with a 250ms head start for private addresses; the first probe whose fingerprint equals the string's (after normalization) wins; mismatched or unreachable candidates are skipped silently; if none match within 10s, show per-address results with the manual path offered. On win: add the machine (auto-pinned fingerprint from the STRING, passcode from the string, hints = parsed order with winner promoted) and call `onPaired`. No visual fingerprint-compare step on this path.

- [ ] **Step 1: Decide placement of the race** by reading how `probeFingerprint` is exposed: if it is per-call over the bridge, the race can live in the renderer (Promise.any over bridge calls with the head-start delay); implement there unless cancellation forces it into main. Record the choice in the commit message body.
- [ ] **Step 2: failing component tests** (Testing Library, existing dialog test file patterns): paste of a valid string with a mocked probe that matches on the second (public) address after the private one fails → machine added with winner first in hints, no compare step rendered; paste where a candidate returns a WRONG fingerprint → that address skipped, no hard-refusal UI, race continues; paste of garbage → inline parse error, manual path still reachable; the pasted string is never rendered back into the DOM after submission (credential hygiene assertion).
- [ ] **Step 3: fail, Step 4: implement, Step 5: green, Step 6: i18n keys in all 8 locales, typecheck 0 errors, Step 7: commit** `feat(desktop): paste a pairing string to add a machine`.

### Task 6: End-to-end pin and docs

**Files:**
- Modify: `frontend/e2e/` pairing spec (extend the existing pair-mode e2e coverage or add `pair-string.spec.ts` using `fake-bridge.ts`), `docs/adr/0004-pairing-string-and-cloud-pair-scope.md` (new), `README.md` (the add-a-machine story), `AGENTS.md` pair-mode carve-out sentence if its wording still says bare-IP is LAN-scoped.

**Interfaces:**
- Consumes: everything above.
- Produces: one e2e driving paste → race (fake bridge returns a matching fingerprint) → machine appears in the picker; ADR 0004 recording (a) the pairing string as the pair-mode front door and (b) the reversal of ADR 0003's "cloud VM unsupported" scoping, cross-referenced from 0003; README's Getting started shows the curl + paste flow (marked as landing with Phase 2 for the curl half).

- [ ] **Step 1: failing e2e, Step 2: implement fake-bridge support, Step 3: green (run the renderer-smoke suite locally), Step 4: write ADR 0004 + README + AGENTS.md wording, Step 5: commit** `feat(desktop): pairing-string e2e, ADR 0004, docs`.

---

## Explicitly out of scope (Phase 2+, do not build here)

- The `get.agentlab.in` installer script and release plumbing (Phase 2).
- mDNS announce/browse.
- Identity-by-fingerprint refactor of `pairedMachineId` (today's address-derived id stays; hints make it survivable until mDNS work).
- Any control-plane/account change.

## Self-review notes

- Task 1's vector file is the contract; Tasks 2, 3, 5 all cite it. If Go emission and TS parsing ever disagree, a vector is missing: add the vector first, then fix.
- Task 5 deliberately keeps the manual flow: `ao pair show` prints no passcode, so a user recovering an address change needs the manual/edit path.
- The racing "wrong cert = skip" rule is discovery-context only and matches the spec's two-context pin semantics; the steady-state hard refusal lives in `paired-machine-cert.ts` and is untouched.
