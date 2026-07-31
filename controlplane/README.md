# controlplane

The hosted AO control plane: brokers Google sign-in, the RFC 8628 device
flow, and JWT issuance for VMs bound via `ao setup-vm`. See
`TOKEN_CONTRACT.md` for the token shapes it issues and
`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md` in
the repo root for the full design.

This service is deployed separately from `backend/`: on the existing Azure
VM it runs as a second Caddy site at `ao.agentlab.in`. Locally it is just a
standalone Go binary with no Caddy in front; hit it directly on
`127.0.0.1:8080`.

## Prerequisites

- Go, matching the version in `go.mod` (currently 1.25.7).

## Configuration

Copy the example env file and fill in the two required Google OAuth values
(see the "Operator setup: Google OAuth client" section of the spec doc for
how to obtain them). The example file already sets the third required
variable, `DATA_DIR`:

```bash
cp .env.example .env
# edit .env, then:
export $(grep -v '^#' .env | xargs)
```

Or export the variables directly:

```bash
export AO_SH_G_CLIENT_ID=your-client-id
export AO_SH_G_CLIENT_SECRET=your-client-secret
export DATA_DIR=./data
```

`DATA_DIR` is required and has no default, because the EdDSA signing keys live
inside it: a working-directory-relative default would silently resolve
elsewhere under a service manager that does not set `WorkingDirectory=`,
generating a fresh key pair and rejecting every already-issued token until each
VM's JWKS cache expires. Use an absolute path in a service unit. The service
logs the resolved absolute data dir and the active `kid` at boot, so an
unintended regeneration is visible.

`LISTEN_ADDR` (default `127.0.0.1:8080`), `PUBLIC_ORIGIN` (default
`https://ao.agentlab.in`, trailing slashes stripped), and `ACCESS_TOKEN_TTL`
(default `15m`, must be between `10m` and `30m`) are optional; see
`internal/config/config.go` for the full list. The service fails to start
(`log.Fatal`) if a Google credential or `DATA_DIR` is missing, or if
`ACCESS_TOKEN_TTL` is out of range.

## Run

```bash
go run ./cmd/controlplane
```

## Verify

```bash
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

Open `http://127.0.0.1:8080/login` in a browser and sign in with Google to
exercise the login flow end to end (see `internal/auth/` for the
authorization-code exchange with PKCE, the `accounts` upsert, and the browser
session cookie). Sign-in lands on `/`, the landing page in `internal/home/`,
which links to the device page. An anonymous `GET /` redirects to `/login`.

## Reachability probe

`ao setup-vm` preflights the VM's public ports with an off-box check: a cloud
firewall is invisible from inside the box, so confirming 80 and 443 needs a
host that is not that box. `internal/reachability/` serves it.

```bash
curl -s 'http://127.0.0.1:8080/api/v1/reachability?host=vm.example.com&ports=80,443'
# {"ports":{"443":true,"80":true}}
```

It is a public service that makes an outbound connection to a caller-supplied
target, so it is an SSRF primitive and is written as one. The hostname is
resolved first and every resolved address is checked against the private,
loopback, link-local (including `169.254.169.254`), CGNAT, multicast, and
IPv6-equivalent ranges; the connection then goes to the address that was
checked rather than to the name, so a second DNS answer cannot redirect it.
Only 80 and 443 are ever dialled, the socket is closed without a read, and the
endpoint is rate limited per client, per target, and globally.

It is unauthenticated: `ao setup-vm` calls it during preflight, before the
device-code binding gives the VM any credential to present. The rate limits are
the whole budget. See the package doc in `internal/reachability/` for the
detail.

## Desktop sign-in

The desktop app signs in with an RFC 8252 loopback authorization-code exchange
with PKCE, implemented in `internal/desktopauth/`. The wire contract is
[`docs/desktop-login-contract.md`](../docs/desktop-login-contract.md) and the
client half is `frontend/src/main/ao-pkce.ts`.

```
GET /oauth/desktop/authorize
  ?response_type=code&client_id=ao-desktop
  &redirect_uri=http://127.0.0.1:<ephemeral>/callback
  &code_challenge=<base64url SHA-256 of the verifier>
  &code_challenge_method=S256&state=<CSPRNG>
```

Opened in the system browser. It reuses `internal/auth`'s Google sign-in and
session (a signed-out request is sent to `/auth/google/login?next=` pointing
back here), then redirects to the loopback listener with a one-time `code` and
the `state` unmodified.

```bash
curl -s http://127.0.0.1:8080/oauth/desktop/token \
  -d grant_type=authorization_code -d client_id=ao-desktop \
  -d code=... -d code_verifier=... \
  -d redirect_uri=http://127.0.0.1:54321/callback
# {"refresh_token":"...","account":{"id":"...","email":"..."}}
```

No access token comes back: login yields identity plus the refresh token, and
every access token comes from `POST /api/v1/token`.

Three things about this endpoint are load-bearing, because it is the only
public unauthenticated entry point that ends in a ninety-day credential.

- **The redirect target is proven loopback before it is used at all**, for a
  code or for an error. `127.0.0.1` and `[::1]` on any port, per RFC 8252
  section 7.3, and nothing else: not `localhost`, which is a name whose
  resolution is not this service's decision, and not any other host, which
  would make this an open redirect that hands out accounts. A refused redirect
  URI, an unknown `client_id`, and a missing `state` are shown to the operator
  in the browser rather than redirected anywhere (RFC 6749 section 4.1.2.1).
- **The code is bound at issue to the PKCE challenge, the redirect URI, and
  the account**, and the exchange re-checks all three against the stored row.
- **The code is consumed in the same transaction that inserts the refresh
  token**, so no interleaving of a replay mints a second one.

An unknown code, an expired one, a replayed one, a mismatched `redirect_uri`,
and a wrong verifier are one `invalid_grant` with one description, the way the
refresh token endpoint collapses its three failures: the distinctions are
exactly the oracle someone holding a stolen code would want.

## Device flow and machine registry

`ao setup-vm` binds a VM to an account with the RFC 8628 device authorization
flow, implemented in `internal/device/`. Both endpoints accept either a
form-encoded or a JSON body.

```bash
# 1. The VM asks for a code. public_url is required and must be a bare origin;
#    machine_name is optional and defaults to the host.
curl -s http://127.0.0.1:8080/device/code \
  -d public_url=https://vm.example.com -d machine_name='prod vm'
# {"device_code":"...","user_code":"WDJB-MJHT",
#  "verification_uri":"http://127.0.0.1:8080/device",
#  "verification_uri_complete":"http://127.0.0.1:8080/device?user_code=WDJB-MJHT",
#  "expires_in":900,"interval":5}

# 2. The human opens verification_uri, signs in, types the code, and approves.

# 3. The VM polls, no faster than `interval` seconds.
curl -s http://127.0.0.1:8080/device/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:device_code \
  -d device_code=...
# {"machine_id":"...","account_id":"...","public_url":"https://vm.example.com"}
```

While the flow is in progress the poll returns HTTP 400 with an RFC 8628 error
code: `authorization_pending` until it is approved, `slow_down` if it is polled
faster than `interval`, `expired_token` once the code's 15 minutes are up, and
`invalid_grant` for a code that was never issued. A denial in the browser
returns 403 `access_denied`.

`machine_id` is `machines.id`. It is the value that goes in `machineId` in
`~/.ao/machine.json` and the `aud` of every access token minted for this
machine; it is never the hostname or the public URL. See `TOKEN_CONTRACT.md`.

That response deliberately carries no access token, unlike the RFC 8628
section 3.5 shape it is otherwise. The VM verifies tokens rather than
presenting them, so `ao setup-vm` never read the one that used to be there:
minting it created and transmitted a live 15 minute credential on every bind
for no consumer. Machine tokens come from the machine token endpoint below.

A successful poll is repeatable until the device code expires, so a dropped
response does not force the operator to approve a second code. Re-running the
device flow for a public URL an account has already bound reuses the existing
machine row, so `ao setup-vm` stays re-runnable without changing the machine
id or duplicating the box in the machine list.

## Control plane API

Every `/api/v1` route that carries a credential takes one: an access token
whose `aud` is the control plane's own origin. A machine-audience token is
rejected, and so is a refresh token, which is presented only at the token
endpoint. See `TOKEN_CONTRACT.md`, "The two audiences". The one route with no
credential at all is the reachability probe above, which is called before a VM
has one.

```bash
# Exchange a refresh token. The refresh token rotates, so persist the
# replacement: the one presented here is already revoked.
curl -s http://127.0.0.1:8080/api/v1/token \
  -d grant_type=refresh_token -d refresh_token=...
# {"access_token":"...","token_type":"Bearer","expires_in":900,"refresh_token":"..."}

curl -s http://127.0.0.1:8080/api/v1/machines -H 'Authorization: Bearer <access token>'
# {"machines":[{"id":"...","name":"prod vm","public_url":"https://vm.example.com",
#               "created_at":"...","last_seen":null}]}

# Ask for a token addressed to one of your machines, so the desktop can call
# that machine's gateway. No request body. Nothing rotates.
curl -s -X POST http://127.0.0.1:8080/api/v1/machines/<machine id>/token \
  -H 'Authorization: Bearer <access token>'
# {"access_token":"...","token_type":"Bearer","expires_in":900}
```

The token that comes back has `aud` = `machines.id` and `sub` = the account
id, so it is a credential for that machine's gateway and is rejected by the
control plane API, which is the point. A machine that belongs to another
account, one that is revoked, and one that does not exist all answer the same
404 `not_found`: distinguishing them would make this a machine-id enumeration
oracle for anyone with an account.

`internal/api` owns both the token endpoint and the authenticator; a feature
package registering an `/api/v1` route takes that authenticator as a value
(see `device.NewService`), so which credential the API accepts is one
substitution rather than an edit per route.

## Test

```bash
go test ./...
```
