# Hosted AO proxy (retired)

> **Retired by spec task 15.** The pairing-secret Caddy path
> (`AO_REMOTE_URL`, `AO_REMOTE_TOKEN`, `ao_hosted_pair` / `AO_HOSTED_PAIR_TOKEN`)
> is no longer supported. Remote mode is accounts only: bind a VM with
> `ao setup-vm`, sign in on the desktop against the control plane, and pick the
> machine. The desktop carries short-lived machine-audience JWTs; the VM runs
> `ao vm serve` on `:80` and `:443`.

## What to use instead

| Concern | Path |
| --- | --- |
| Control plane (login, device flow, machines) | `https://ao.agentlab.in` and `controlplane/` + `deploy/controlplane/` |
| User VM public edge | `ao setup-vm` installs `ao-daemon` + `ao-gateway` (`ao vm serve`) |
| Desktop remote | Sign in → machine picker → machine-audience token (no env pairing) |
| Development hatch | `AO_CONTROL_URL` selects which control plane to trust; it never skips auth |

## Historical files in this directory

`Caddyfile` used to be kept here only so old deployments could be recognized
and torn down, but a valid, installable config referencing a retired
pairing-token scheme is a port-collision hazard against exactly the ports
`ao setup-vm`'s preflight protects: Caddy and `ao vm serve` both want
`:80`/`:443`, and nothing stopped someone from copy-pasting it onto a new VM.
It has been deleted; the old contents are still in git history
(`git log --follow -- deploy/hosted/Caddyfile`) for anyone recognizing a
leftover deployment. Do not restore it as a runnable file.

To retire a leftover pairing proxy on an existing box:

1. Stop and disable the Caddy unit that served `api.ao.agentlab.in` with the
   pairing cookie matcher.
2. Ensure `ao-gateway.service` (`ao vm serve`) is the public listener.
3. Remove any desktop launch wrappers that still export `AO_REMOTE_URL` /
   `AO_REMOTE_TOKEN`.

Architecture: [`docs/adr/0002-hosted-public-gateway.md`](../../docs/adr/0002-hosted-public-gateway.md).
Verification writeup: [`hosted-log.md`](../../hosted-log.md).
