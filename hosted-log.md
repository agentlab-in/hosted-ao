# Hosted AO log, 2026-07-27

> **Superseded in intent, not yet replaced in fact (noted 2026-07-30).** This log records the
> single shared pairing-secret deployment, which was the whole hosted story on 2026-07-27.
> Hosted AO v1 (accounts, registered machines, per-machine tokens, `ao setup-vm` and
> `ao vm serve`) replaces it, and 12 of its 15 spec tasks are merged on `develop`. **None of
> that has run on a VM yet**, so nothing below has been re-verified against it and nothing
> below has been overwritten with results that have not happened.
>
> Spec task 14 owns the replacement: run the full accounts flow on a clean Ubuntu LTS VM,
> then write up what was actually verified. That write-up must cover, at minimum, ACME
> issuing a certificate for the machine's domain, the RFC 8628 device flow completing against
> real DNS, `machine.json` written and read back by the gateway, the desktop reaching the
> machine over the gateway with a machine-audience token, and `/mux` over WSS with the
> `ao_gw_token` cookie. Until then, treat this file as the record of the old path.
>
> See [`docs/hosted-ao-v1-build-log.md`](docs/hosted-ao-v1-build-log.md) and
> [`docs/STATUS.md`](docs/STATUS.md).

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
  isolation yet. Hosted AO v1 adds all three; it is not deployed here.
- Browser-preview proxying is intentionally out of scope.
- Implementation work now happens on `develop`, not `main`, and this repository
  is no longer a fork.
