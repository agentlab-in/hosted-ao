# Hosted AO proxy

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

# VM: load Caddy with AO_HOSTED_PAIR_TOKEN exported by its systemd EnvironmentFile.
sudo caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
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

Create an A record for `api.ao.agentlab.in` that points to `YOUR_VM_PUBLIC_IP`.
Allow TCP ports 80 and 443 through both the Azure NSG and the VM host firewall.
Do not expose port 3001. Do not add a public daemon bind, a second daemon
listener, or a proxy route for a browser preview server.

`app://renderer` is already the Go daemon's default allowed CORS origin. Do
not set `AO_ALLOWED_ORIGINS` on the VM in a way that overwrites that default.

## Manual end-to-end smoke test

After copying the Caddyfile to the VM and exporting a non-secret test token in
the Caddy service environment, validate the configuration without starting
deployment services:

```bash
sudo caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
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
