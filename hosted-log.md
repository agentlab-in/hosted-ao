# Hosted AO log, 2026-07-31

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
  from `controlplane/`, `scp` to the Azure VM, `install -m 0755` to
  `/opt/ao-controlplane/controlplane`, `systemctl restart ao-controlplane`.
  Signing keys live under `/var/lib/ao-controlplane` and survive restarts, so
  already-issued tokens keep verifying.
- Restoring a user VM from a boot-disk snapshot is safe: `machine.json`, the
  systemd units, the cached ACME cert, and the daemon's SQLite state all come
  back. Only the DNS A record has to be re-pointed at the new external IP.
- Claude Code auth is per Unix user. If the interactive login runs under a
  different user than the daemon (easy to do over GCP browser SSH, which uses
  the operator's Google login as the Linux user), copy `~/.claude/` to the
  daemon user, `chown -R`, and `ao doctor` flips to PASS.

## Bugs found during verification

Patched locally, not yet merged:

- [#64](https://github.com/agentlab-in/hosted-ao/issues/64): `ao vm serve`
  hardcodes `DefaultAllowedOrigins` instead of honoring `AO_ALLOWED_ORIGINS`.
  Blocks `npm run dev`, worked around on the test VM with a systemd
  `Environment=` override.
- [#65](https://github.com/agentlab-in/hosted-ao/issues/65): `createProject`
  in `frontend/src/renderer/routes/_shell.tsx` requires `status.port`, which is
  only set for the local daemon. Every remote clone and remote session spawn
  threw before any fetch went out. Local one-line fix accepts `status.baseUrl`
  as ready too.
