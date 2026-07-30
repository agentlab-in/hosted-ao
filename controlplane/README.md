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
how to obtain them):

```bash
cp .env.example .env
# edit .env, then:
export $(grep -v '^#' .env | xargs)
```

Or export the variables directly:

```bash
export AO_SH_G_CLIENT_ID=your-client-id
export AO_SH_G_CLIENT_SECRET=your-client-secret
```

`LISTEN_ADDR` (default `127.0.0.1:8080`), `DATA_DIR` (default `./data`), and
`PUBLIC_ORIGIN` (default `https://ao.agentlab.in`) are optional; see
`internal/config/config.go` for the full list. The service fails to start
(`log.Fatal`) if either Google credential is missing.

## Run

```bash
go run ./cmd/controlplane
```

## Verify

```bash
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

## Test

```bash
go test ./...
```
