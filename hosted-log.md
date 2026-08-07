# Hosted AO log

Newest first. One entry per operating day that changed the system.

# 2026-08-08: real ancestry, upstream caught up, control plane extracted

The repo's git history was rebuilt, upstream was merged in full, the app was
renamed, three cloud-mode interaction bugs were found and fixed against the
live VM, and the control plane moved to a private repo. The public repo went
from 874 branches and 252 tags to 3 branches and 0 tags.

## History surgery

hosted-ao's history was a copied lineage (the AgentWrapper/ReverbCode graft),
not upstream's real commits; the only common ancestor with
`Untrivial-ai/agent-orchestrator` was the initial scaffold, so every merge
computation was garbage. The fix was cheap because the last copied commit
(`6d3c3c5a0`) had a byte-identical tree to upstream's real `115d219eb`:
branch from the real commit, lay a tree-exact port commit for the pre-v1
layer, cherry-pick the 44 v1 commits verbatim. Final tree byte-identical to
the old develop; merge-base with upstream became real.

## Upstream merge

All 218 upstream commits since the fork point, one merge commit (`52efa6e`
pre-scrub). 60 both-touched files, 30 with conflicts. Notable resolutions:

- Doctor: the hosted `internal/doctor` extraction stayed the single engine
  behind `ao doctor`, `GET /api/v1/doctor`, and the VM preflight; upstream's
  delta was only the muse probe with its version-prefix guard, folded in.
- `telemetrymeta`: hosted CLI commands registered (`ao vm serve` as system,
  `setup-vm`/`vm`/`vm setup-harness`/`whoami` as user).
- The foreign-history guard dropped `worker_idle_events` from its proof set:
  upstream's migration 0037 legitimately drops that table, so its absence no
  longer proves a foreign database.
- Migrations did not collide: hosted migrations live only in the control
  plane's own goose history. `openapi.yaml`, `schema.ts`, and sqlc output
  regenerated rather than merged.

## Renamed to Hosted AO

`Hosted AO.app`, executable `hosted-ao`, bundle id
`in.agentlab.hosted-ao.desktop`, updater cache `hosted-ao-updater`, release
repo default `agentlab-in/hosted-ao`. Coexists with a stock Agent
Orchestrator install; state was already isolated under `~/.ao/hosted`.

## Three interaction bugs, found against the live VM

None of these were visible to CI; all three only exist when the merged
desktop talks to a real remote machine.

1. **Stream auth** (`102662f` pre-scrub): upstream added EventSource surfaces
   beyond `/api/v1/events` (`notifications/stream`, per-session
   `workspace/events`). EventSource cannot send a Bearer header and the
   gateway accepted the cookie only on `/mux` and `/api/v1/events`, so the
   new streams 401ed in a retry loop. The gateway now enumerates the
   EventSource routes for cookie auth; the workspace stream sends
   credentials on remote base URLs.
2. **Status push feedback** (same commit): `setDaemonStatus` pushed to the
   renderer on every call and remote statuses are rebuilt per read, so every
   status read caused a push, every push caused query refetches, and
   refetches caused more reads. Pushes now fire only on content change.
3. **Reachability reset loop** (`e4b883d` pre-scrub): every machines refresh
   rebuilt the list with `reachability: "unknown"`, flipping the active
   machine connecting/ready twice per refresh. Each flip re-pointed the
   renderer base URL, each re-point ran `queryClient.clear()`, and the
   machines list is itself a cleared query, so clearing scheduled the next
   refresh. Steady state was ~1,800 aborted requests per 6 seconds
   (`net::ERR_INSUFFICIENT_RESOURCES`) and enough pressure to take the
   window down. Relisting now preserves probed reachability; one refresh
   produces at most one real transition. Idle traffic after: ~zero, streams
   held open.

Debugging gotchas that cost real time: `app.moveToApplicationsFolder()`
relocates any copy launched outside /Applications (bundles "vanish"), and
System Events window counts only see the current macOS Space, so a fullscreen
editor makes a healthy window read as missing.

## VM redeploy

`vm-shared.ao.agentlab.in` (Azure, x86_64) runs the merged binary:
`scp` to `/tmp`, `install -m 0755` to `/usr/local/bin/ao`, restart
`ao-daemon` then `ao-gateway`. The daemon migrated the VM's SQLite forward on
first start through upstream's repair chain. `ao doctor` over SSH WARNs on
claude because the non-interactive SSH PATH lacks `~/.local/bin`; the systemd
unit pins PATH correctly and the daemon-side doctor passes.

## Control plane extracted

The repo is public and the control plane is the hosted business layer, so it
moved to the private `agentlab-in/ao-controlplane`: full history via subtree
split (14 commits) plus the deploy files (systemd unit, Caddyfile site, env
template). In hosted-ao it was removed from the tip and scrubbed from all of
history with `git filter-repo --path controlplane --invert-paths` bounded to
`53197448f..merging`; bounding matters, the two obvious ref specs each
rewrote upstream's commits and broke ancestry before the right one held.
The token contract stays executable from this side: the golden fixtures are
vendored under `backend/internal/vmgateway/testdata` and regenerate in the
private repo.

## Repo cleanup

`develop` force-pushed to the new lineage (ruleset toggled around the push)
and remains the default branch. 871 stale branches and 252 tags deleted, SHA
backups in `~/hosted-ao-branch-backup-20260808.txt` and
`~/hosted-ao-tags-backup-20260808.txt`. README rewritten for the Hosted AO
story; the seven translated upstream READMEs removed.

## Open

- `main` still points at the old lineage (control plane included); repoint or
  delete.
- Six clone-UI strings are hardcoded English; the i18n coverage test is the
  only red test, deliberately deferred.
- The auto-updater 404s against a stale nightly tag; silence or point it at a
  real release.
- Old branch/tag objects may persist in GitHub caches until a support gc.

# 2026-07-31: v1 verified end to end

Hosted AO v1 verified end to end on a real user-owned VM. From a signed-out
desktop:

1. Sign in with Google against `https://ao.agentlab.in` (system browser, PKCE,
   loopback redirect).
2. The account's registered machines appear in the app; select the remote one.
3. Add a project by Git URL (`https://github.com/agentlab-in/journal`) and the
   daemon on the VM clones it.

Everything travels over `https://vm-test.ao.agentlab.in` with a machine-audience
JWT: `Authorization: Bearer` for REST, `ao_gw_token` cookie for `/mux` and SSE.

## Architecture

- Control plane at `https://ao.agentlab.in` on the Azure VM: Go binary at
  `/opt/ao-controlplane/controlplane`, systemd unit `ao-controlplane.service`,
  Caddy in front for TLS. Owns Google login, RFC 8628 device flow, machines
  registry, EdDSA JWKS, refresh-token exchange.
- User VM (GCP `ao-test-vm` in this test): the `ao` binary runs both the
  loopback daemon on `127.0.0.1:3001` (`ao-daemon.service`) and the public TLS
  gateway (`ao-gateway.service`, `ao vm serve`) bound on `:80` and `:443`. The
  gateway verifies AO tokens against the JWKS, checks `aud`/`sub`, and
  reverse-proxies authenticated requests to the loopback daemon.
- Desktop app: signed-in state under `~/.ao/hosted/dev/ao-account.json`
  (safeStorage-encrypted refresh token), active machine in `ao-machine.json`,
  data root `~/.ao/hosted/*` (isolated from the upstream AO build).

## Verified

- Control plane: `/oauth/desktop/authorize` 302, `/oauth/desktop/token` 400 on
  bad grant, `/api/v1/machines` 401 unauth, `/api/v1/token` 400 on invalid
  refresh, `/api/v1/reachability` 200, `/login` 200, `/` 302.
- Gateway on the user VM: TLS 1.3 with a Let's Encrypt cert for
  `vm-test.ao.agentlab.in`, deny-by-default 404 on `/healthz`, 401 on
  `/api/v1/*` without a token, 401 on the SSE stream without the cookie, CORS
  preflight 204 with `Access-Control-Allow-Origin: app://renderer`.
- Desktop end to end: Google sign-in through the loopback callback, machine
  list populated from the account, machine-audience token exchange, remote
  clone of a public Git URL onto the VM.
- Claude Code auth on the VM: `ao doctor` reports `PASS claude-auth` for the
  daemon user; `claude --print` under that user completes.

## Operator notes

- Redeploying the control plane: cross-compile
  `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/controlplane ./cmd/controlplane`
  from the `agentlab-in/ao-controlplane` repo, `scp` to the Azure VM,
  `install -m 0755` to `/opt/ao-controlplane/controlplane`,
  `systemctl restart ao-controlplane`. Signing keys live under
  `/var/lib/ao-controlplane` and survive restarts, so already-issued tokens
  keep verifying.
- Restoring a user VM from a boot-disk snapshot is safe: `machine.json`, the
  systemd units, the cached ACME cert, and the daemon's SQLite state all come
  back. Only the DNS A record has to be re-pointed at the new external IP.
- Claude Code auth is per Unix user. If the interactive login runs under a
  different user than the daemon (easy to do over GCP browser SSH, which uses
  the operator's Google login as the Linux user), copy `~/.claude/` to the
  daemon user, `chown -R`, and `ao doctor` flips to PASS.

## Bugs found during verification

Patched during the v1 build (both closed):

- [#64](https://github.com/agentlab-in/hosted-ao/issues/64): `ao vm serve`
  hardcodes `DefaultAllowedOrigins` instead of honoring `AO_ALLOWED_ORIGINS`.
  Fixed in [#66](https://github.com/agentlab-in/hosted-ao/pull/66).
- [#65](https://github.com/agentlab-in/hosted-ao/issues/65): `createProject`
  in `frontend/src/renderer/routes/_shell.tsx` requires `status.port`, which is
  only set for the local daemon. Every remote clone and remote session spawn
  threw before any fetch went out. Fixed in
  [#67](https://github.com/agentlab-in/hosted-ao/pull/67).
