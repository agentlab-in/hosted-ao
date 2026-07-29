# Hosted AO v1: interview questions

Answer inline, or reply "defaults except 7, 26" and only argue what you disagree
with. Load-bearing questions are marked ⚠️.

## Grounding

What exists today in this repo:

- `docs/superpowers/specs/2026-07-26-hosted-remote-daemon-design.md`, the phase 1
  design.
- `frontend/src/shared/remote-daemon.ts`, env-var config, the `ao_hosted_pair`
  cookie name, HTTPS-origin validation.
- `deploy/hosted/`, the Caddy configuration.
- The Go daemon, untouched, on loopback.
- `POST /api/v1/projects` is `addProject`. Nothing in the backend clones a repo.
- AGENTS.md hard rule: the loopback listener stays unauthenticated and no other
  network-facing bind is added. The Caddy design respects this by staying
  outside the binary.

What does not exist: agentlab.in's infrastructure was removed, so there is no
auth, no database, and no deployment to reuse. The control plane is greenfield.
That makes this three pieces of work, not one: a new control plane, the VM
tooling, and the desktop app changes.

**Question 0:** you said the sharpened intent was "approximately" what you want.
What is the delta? If it is nothing real, say so and I will drop it.

## A. The control plane (all new)

The v1 surface is small: Google OAuth login, a device-code endpoint pair, a
machines table, a JWKS endpoint, a token endpoint, and two web pages (login,
enter device code).

1. ⚠️ **Stack: one Go service using `html/template` for the two pages, with
   SQLite for storage.** Same language as AO, single binary, no Node build, no
   Postgres to operate. The alternative is Next.js plus Auth.js plus hosted
   Postgres, which is more familiar for web but three more moving parts for two
   pages. Recommendation: the Go service. Take Next.js only if a dashboard and
   billing UI are landing within weeks.
2. ⚠️ **It lives in this repo under `controlplane/`, deployed separately**,
   rather than in a new repo. AO workers building this all operate in one
   checkout, one CI, one review flow. Two repos double the orchestration for no
   benefit at this size.
3. **It runs on the Azure VM you already have, as a second Caddy site at
   `ao.agentlab.in`.** Cheap, and you already know that deployment. Noting the
   coupling explicitly: your test VM becomes the thing every user's login
   depends on. Acceptable for v1?
4. **Storage: SQLite, tables `accounts`, `machines`, `device_codes`,
   `refresh_tokens`.**
5. **Signing keys: EdDSA, generated at first boot, stored on disk, published at
   `/.well-known/jwks.json`**, with a next-key slot for rotation.
6. **You create the Google Cloud OAuth client by hand** (project, consent
   screen, redirect URI). No worker can do this, and it blocks the login task.

## B. The auth mechanism on the VM

7. ⚠️ **The biggest fork.** Caddy cannot verify a JWT without a plugin, so there
   are two shapes:
   - **B1. Keep Caddy, add `forward_auth` to a small `ao-hosted-auth` Go
     sidecar** that verifies the token. Preserves everything already deployed
     and verified. Cost: three processes (Caddy, sidecar, daemon), and
     `setup-vm` has to install and template all of them.
   - **B2. Replace Caddy with a gateway inside the `ao` binary**
     (`ao vm serve`), using certmagic or `autocert` for ACME, verifying the
     token in Go and proxying to loopback. Cost: throw away working Caddy config
     and own ACME. Benefit: `setup-vm` installs one binary and one systemd unit,
     no Caddyfile templating, no journal-leak workaround.

   Recommendation: **B2**, because the sidecar in B1 is a new Go service anyway,
   so B1 is strictly more moving parts than B2 for the same code. B1 is still
   reasonable if you want v1 to reuse what is already verified.
8. Either way, **AGENTS.md's "no other network-facing bind" hard rule needs an
   ADR amendment.** B2 binds :443 from the ao binary; B1 keeps it external but
   moves the auth boundary. Write `docs/adr/0002-hosted-public-gateway.md` as
   part of this work?
9. **Access token: JWT, `aud` = machine id, 15 minute expiry.** Browsers cannot
   set headers on a WebSocket handshake, so the default is: **keep a cookie for
   `/mux`, bearer header elsewhere.** Simplest alternative is to keep the cookie
   everywhere and make its value a short-lived JWT.
10. **The VM's allowlist is a single account id written at setup time**, checked
    against the token's subject.
11. **Refresh: the desktop holds a long-lived refresh token from AO login and
    exchanges it for access tokens.** Stored via Electron `safeStorage`,
    ciphertext under `~/.ao/`, per the state hard rule.
12. **Clock skew tolerance 60s. The VM caches JWKS for 1h with a
    stale-if-error fallback**, so a brief control-plane outage does not break
    connected users.

## C. Device code binding

13. **Implement RFC 8628 device authorization grant** properly (user code,
    verification URI, 5s poll interval, 10 minute expiry) rather than a
    homegrown code. Known-good spec, and users already recognize the GitHub
    flow.
14. **After binding, the VM writes `~/.ao/machine.json` (0600)**: machine id,
    account id, public URL, issued at.
15. **One machine binds to exactly one account in v1.** No teams, no sharing.
16. **`ao whoami` prints account email, machine id, public URL, and bound-at
    time**, and exits non-zero if unbound.
17. **Re-running `ao setup-vm` on a bound machine re-binds** rather than
    erroring, and prints the previous binding first.

## D. What `ao setup-vm` actually does

18. **Ubuntu LTS with systemd and apt only in v1.** Everything else prints
    "unsupported, here is the manual path".
19. **It runs on the VM, invoked inside the user's own SSH session**, not driven
    remotely from the laptop. Remote-drive-over-SSH is a much bigger surface and
    I would park it.
20. **Preflight before touching anything**: resolve the given hostname, confirm
    it points at this box's public IP, confirm 80 and 443 are reachable from
    outside, confirm sudo. Fail with exact remediation text and change nothing.
    The cloud firewall is the one thing the script cannot fix, so it must detect
    and instruct.
21. **It installs: the `ao` binary, tmux, git, gh, a systemd unit for the
    daemon, and the gateway** (Caddy or `ao vm serve`, per Q7). Harnesses are
    not installed here.
22. **It is idempotent and re-runnable**, and prints a final summary listing
    what is still missing (no harness, no git auth).

## E. Harness and git auth

23. **`ao vm setup-harness <name>` runs the harness's own interactive login in
    the foreground** and does not try to script or scrape it. v1 supports
    `claude` only, with the command shape generalized. Do you want codex in v1
    too?
24. **`ao vm setup-git` wraps the `gh auth login` device flow**, and the
    daemon's existing `gh auth token` path (already in
    `backend/internal/daemon/tracker_intake_wiring.go`) picks the credential up
    for free. Confirming explicitly because it makes `gh` a hard dependency on
    the VM.
25. **The app reads harness and git readiness from the existing `ao doctor`
    checks** and shows them on the machine card. Tell me if there is a better
    existing endpoint than doctor, since you said there is already a good way to
    check whether a harness is logged in.

## F. Projects by git URL

26. ⚠️ **Extend `POST /api/v1/projects` with an optional `cloneUrl`** rather
    than adding a new endpoint, and have the daemon clone then register.
27. **This works in local mode too**, not gated to remote. Same code path, and
    it is useful on a laptop.
28. **Clones land in `~/.ao/repos/<owner>-<repo>`**, per the state hard rule.
29. **Clone runs synchronously with a generous timeout** for v1. Async with
    progress needs a job model that does not exist yet. Push back if a
    multi-minute blocking call is unacceptable.
30. **Clone failure returns exact remediation** ("run `ao vm setup-git` on this
    machine"), not a raw git error.

## G. Desktop app

31. ⚠️ **Login is required only for remote machines. Local use never requires an
    account.** "This Mac" is always present as machine zero with no login. This
    is the one I would refuse to compromise on: making people log in to use a
    local tool is a real regression.
32. **One active machine at a time.** Switching machines reloads the renderer
    against the new base URL. Multiple machines side by side is out of scope.
33. **Keep `AO_REMOTE_URL` / `AO_REMOTE_TOKEN` working as a dev escape hatch**
    after the login path exists.
