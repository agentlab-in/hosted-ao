# Token contract

This is the shared token contract for the control plane, the VM gateway
(`ao vm serve`), and the desktop transport. It is defined once so the three
can be built in parallel. It must stay in sync with the "Token contract"
section of
[`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md`](../docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md),
which it was copied from; if that section changes, update this file to match.

- Access token: JWT, EdDSA, `iss` = `https://ao.agentlab.in`, `sub` = account
  id, `aud` = machine id, `exp` = 15 minutes, `iat`, `jti`.
- Refresh token: opaque, high entropy, stored hashed in `refresh_tokens`, bound
  to an account and a desktop install, long-lived, revocable.
- Transport: `Authorization: Bearer <jwt>` for REST and SSE. Browsers cannot set
  headers on a WebSocket handshake, so `/mux` continues to use a cookie, whose
  value is the same short-lived JWT rather than a shared secret. The cookie is
  Secure, HttpOnly, host-only, and installed by the Electron main process.
- Verification on the VM: signature against cached JWKS, `iss`, `aud` equal to
  this machine's id, `exp` with 60s skew tolerance, and `sub` equal to the
  single account id in the machine's allowlist.
- JWKS cache: 1 hour, with stale-if-error so a brief control-plane outage does
  not disconnect working users.
