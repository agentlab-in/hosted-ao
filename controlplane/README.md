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

## Test

```bash
go test ./...
```
