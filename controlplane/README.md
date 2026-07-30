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
session cookie).

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
# {"access_token":"...","token_type":"Bearer","expires_in":900,
#  "machine_id":"...","account_id":"...","public_url":"https://vm.example.com"}
```

While the flow is in progress the poll returns HTTP 400 with an RFC 8628 error
code: `authorization_pending` until it is approved, `slow_down` if it is polled
faster than `interval`, `expired_token` once the code's 15 minutes are up, and
`invalid_grant` for a code that was never issued. A denial in the browser
returns 403 `access_denied`.

`machine_id` is `machines.id`. It is the value that goes in `machineId` in
`~/.ao/machine.json` and the `aud` of the access token in the same response;
it is never the hostname or the public URL. See `TOKEN_CONTRACT.md`.

A successful poll is repeatable until the device code expires, so a dropped
response does not force the operator to approve a second code. Re-running the
device flow for a public URL an account has already bound reuses the existing
machine row, so `ao setup-vm` stays re-runnable without changing the machine
id or duplicating the box in the machine list.

## Control plane API

Every `/api/v1` route takes one credential: an access token whose `aud` is the
control plane's own origin. A machine-audience token is rejected, and so is a
refresh token, which is presented only at the token endpoint. See
`TOKEN_CONTRACT.md`, "The two audiences".

```bash
# Exchange a refresh token. The refresh token rotates, so persist the
# replacement: the one presented here is already revoked.
curl -s http://127.0.0.1:8080/api/v1/token \
  -d grant_type=refresh_token -d refresh_token=...
# {"access_token":"...","token_type":"Bearer","expires_in":900,"refresh_token":"..."}

curl -s http://127.0.0.1:8080/api/v1/machines -H 'Authorization: Bearer <access token>'
# {"machines":[{"id":"...","name":"prod vm","public_url":"https://vm.example.com",
#               "created_at":"...","last_seen":null}]}
```

`internal/api` owns both the token endpoint and the authenticator; a feature
package registering an `/api/v1` route takes that authenticator as a value
(see `device.NewService`), so which credential the API accepts is one
substitution rather than an edit per route.

## Test

```bash
go test ./...
```
