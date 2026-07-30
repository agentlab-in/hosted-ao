# Hosted AO proxy

> **Scope note (2026-07-30).** This file documents the **pairing-secret Caddy proxy**, the
> pre-accounts hosted path. It is still the only path that has ever run on a VM, and it keeps
> working on purpose: spec task 15 removes `AO_REMOTE_URL`, `AO_REMOTE_TOKEN`, and the
> `ao_hosted_pair` secret only after the fresh-VM run (task 14) passes, so remote mode is
> never untestable mid-build.
>
> The replacement is `ao setup-vm` plus `ao vm serve`: per-account registered machines and
> short-lived per-machine JWTs instead of one shared secret, and the `ao` binary itself
> holding `:80` and `:443` instead of Caddy. It is merged but **unrun**; task 14 owns
> standing it up on a clean Ubuntu LTS VM and writing the deployment guide for it. Do not
> read the accounts path out of this file, and do not add results here that have not
> happened. See [`docs/adr/0002-hosted-public-gateway.md`](../../docs/adr/0002-hosted-public-gateway.md)
> and [`docs/hosted-ao-v1-build-log.md`](../../docs/hosted-ao-v1-build-log.md).

This deployment exposes the AO daemon on a VM through Caddy at
`https://api.ao.agentlab.in`. The daemon remains loopback-only on
`127.0.0.1:3001`; Caddy is the only public listener and permits requests only
when their `ao_hosted_pair` cookie exactly matches `AO_HOSTED_PAIR_TOKEN`.

## VM setup

Install the supplied `Caddyfile` as `/etc/caddy/Caddyfile`. Create the pairing
secret once, store it in a root-readable systemd environment file, and use the
same value as the local desktop's `AO_REMOTE_TOKEN`.

```bash
# VM: create a URL-safe pairing secret once and store it in a root-readable env file.
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='

# VM: run the existing daemon only on loopback.
AO_PORT=3001 ao daemon

# VM: validate with the same root-only environment file that systemd loads.
sudo sh -c 'set -a; . /etc/caddy/hosted-ao.env; caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile'
sudo systemctl reload caddy

# Mac: launch the desktop build in remote mode.
AO_REMOTE_URL=https://api.ao.agentlab.in AO_REMOTE_TOKEN="$AO_HOSTED_PAIR_TOKEN" npm run dev
```

Do not put the generated secret in shell history, logs, or this repository. Its
URL-safe form (`A-Z`, `a-z`, `0-9`, `-`, and `_`) is safe to use literally in
the Caddy cookie-matching regular expression. Create `/etc/caddy/hosted-ao.env`
with mode `600`, owned by `root`, containing
`AO_HOSTED_PAIR_TOKEN=<generated-secret>`. Configure Caddy with this systemd
drop-in:

```ini
# /etc/systemd/system/caddy.service.d/hosted-ao.conf
[Service]
EnvironmentFile=/etc/caddy/hosted-ao.env
ExecStart=
ExecStart=/usr/bin/caddy run --config /etc/caddy/Caddyfile
```

Then run `sudo systemctl daemon-reload && sudo systemctl restart caddy`. The
`ExecStart` replacement deliberately removes Ubuntu's default `--environ` flag,
which would otherwise write the pairing secret to Caddy's journal. Caddy still
retains access to its certificate storage for TLS issuance and renewal.
The supplied Caddyfile uses Caddy's `{$AO_HOSTED_PAIR_TOKEN}` parse-time
substitution, which is required for the cookie regular-expression matcher.

Create an A record for `api.ao.agentlab.in` that points to `YOUR_VM_PUBLIC_IP`.
Allow TCP ports 80 and 443 through both the Azure NSG and the VM host firewall.
Do not expose port 3001. Do not add a public daemon bind, a second daemon
listener, or a proxy route for a browser preview server. The one permitted
network-facing bind besides this proxy is `ao vm serve`, the VM gateway, which
is a separate process from the daemon and is covered by ADR 0002; that carve-out
is in `AGENTS.md` and does not widen anything here. Caddy and `ao vm serve` both
want `:80` and `:443`, so they cannot run on the same VM at the same time.

`app://renderer` is already the Go daemon's default allowed CORS origin. Do
not set `AO_ALLOWED_ORIGINS` on the VM in a way that overwrites that default.

## Manual end-to-end smoke test

After copying the Caddyfile to the VM and exporting a non-secret test token in
the Caddy service environment, validate the configuration without starting
deployment services:

```bash
sudo sh -c 'set -a; . /etc/caddy/hosted-ao.env; caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile'
```

Expected: Caddy reports that the configuration is valid. Do not print the
token in terminal output.

Then perform these checks in order:

1. `curl -i https://api.ao.agentlab.in/healthz` returns `401` without the cookie.
2. `curl -i --cookie "ao_hosted_pair=$AO_HOSTED_PAIR_TOKEN" https://api.ao.agentlab.in/healthz` returns the daemon health response.
3. Start the local Electron app with the two remote variables and verify the sidebar displays a project stored on the VM.
4. Change a VM session through the AO API or CLI and verify the desktop board refreshes without a manual reload; this verifies the credentialed SSE connection.
5. Open that session's terminal in Electron, type `pwd`, and verify the output reports the VM workspace path; this verifies the WSS mux and pairing cookie on the upgrade request.
6. Quit the app, start it with no remote variables, and verify a local daemon is discovered or started as before.
