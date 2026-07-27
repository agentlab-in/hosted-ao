# Hosted AO log — 2026-07-27

## Outcome

The first hosted AO slice is live for testing at `https://api.ao.agentlab.in`.
The Electron app can run locally against the VM-backed AO daemon.

## Architecture

- AO daemon runs on the Azure VM as `hosted-ao.service`, bound only to
  `127.0.0.1:3001`.
- Caddy runs as the public TLS edge at `api.ao.agentlab.in`.
- Caddy requires the `ao_hosted_pair` cookie for app requests and proxies REST,
  SSE, and terminal mux traffic to the loopback daemon.
- Electron remote mode uses `AO_REMOTE_URL` and `AO_REMOTE_TOKEN`; the token is
  installed as a Secure, HttpOnly, host-only cookie, so the renderer never sees
  it.
- Browser CORS preflights are allowed without the pairing cookie; all actual
  app requests require it. Loopback-only control routes are blocked by Caddy.

## VM preparation

- DNS: `api.ao.agentlab.in` points to `YOUR_VM_PUBLIC_IP`.
- Azure inbound TCP 80/443 is enabled; Caddy obtained a trusted Let's Encrypt
  certificate.
- `tmux 3.4` is installed.
- Claude Code is installed for `azureuser` but still requires user
  authentication.
- Caddy's systemd override avoids its default `--environ` flag so the pairing
  token is not written to the journal.

## Verified

- Unpaired health request: `401`.
- Paired health request: `200`.
- Credentialed CORS preflight: `204` with credential support.
- Bare unauthenticated `OPTIONS`: `401`.
- `/shutdown` through the public proxy: `404`.
- SSE stream: `200 text/event-stream`.
- Terminal mux WebSocket upgrade: `101`.
- Electron dev app successfully connected to the hosted backend.

## Current limits

- This is a single shared pairing-secret setup: no accounts, roles, or tenant
  isolation yet.
- Browser-preview proxying is intentionally out of scope.
- Keep future implementation work on this fork's `main` branch.
