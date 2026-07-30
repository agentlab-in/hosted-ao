# Token contract

This is the shared token contract for the control plane, the VM gateway
(`ao vm serve`), and the desktop transport. It is defined once so the three
can be built in parallel. It was copied from the "Token contract" section of
[`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md`](../docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md);
where this file is more specific than that section, this file is the one to
build against, and if that section changes, update this file to match.

- Access token: JWT, EdDSA, `iss` = `https://ao.agentlab.in`, `sub` = account
  id, `exp` = 15 minutes, `iat`, `jti`, and an `aud` that is either a machine
  id or the control plane's own origin. See "The two audiences" below. When
  the audience is a machine it is the machine id (`machines.id`), never the
  machine's hostname or public URL.
- Access token lifetime: 15 minutes by default, configurable via
  `ACCESS_TOKEN_TTL` but only within 10 to 30 minutes. Nothing checks an
  access token against a revocation list, so the lifetime is the whole
  revocation window; the control plane refuses to start outside that range.
- Refresh token: opaque, high entropy, stored hashed in `refresh_tokens`,
  bound to an account and a desktop install, revocable.
- Refresh token lifetime: 90 days from issuance, and it **rotates on every
  use**. Exchanging a refresh token revokes the presented one in the same
  transaction that issues its replacement, so a refresh token is single-use
  and a replayed one is rejected as revoked rather than honoured. The desktop
  install must therefore persist the replacement it gets back on every
  refresh, and the 90 days mostly bound how long an install may go without
  contacting the control plane at all.

## The two audiences

There are exactly two access token audiences. They differ only in `aud`, and
they are **not interchangeable in either direction**.

| `aud`                       | Means                          | Verified by                    |
| --------------------------- | ------------------------------ | ------------------------------ |
| `machines.id`               | "call that machine's gateway"  | `ao vm serve` on that VM       |
| `https://ao.agentlab.in`    | "call the control plane's API" | the control plane itself       |

The control-plane audience is the control plane's own origin, the same string
as `iss`, rather than a separate identifier: there is exactly one control
plane and it already publishes that origin everywhere.

- A **control-plane token replayed against a VM** fails that gateway's `aud`
  check against its own machine id, and a **machine token presented to the
  control plane API** fails the control plane's. Neither rejection needs any
  coordination between the two services beyond this table. Both are the
  security property, not a bug to accommodate.
- Everything else is identical: same EdDSA signing key and JWKS, same `iss`,
  same 15 minute default lifetime, same `sub`, `iat`, and `jti`.
- A verifier compares `aud` byte for byte against the single value it accepts.
  Neither side accepts a list, a wildcard, or a missing `aud`.

### How each is obtained

- **Machine audience:** from the RFC 8628 device flow, at the end of
  `ao setup-vm`. The polling response carries the machine id, the account id,
  and the public URL, which is what `~/.ao/machine.json` is written from.
- **Control-plane audience:** by exchanging a refresh token at
  `POST /api/v1/token` (`grant_type=refresh_token`), which rotates the refresh
  token in the same response.

### Where a refresh token may be presented

A refresh token goes to the token endpoint and **nowhere else**. It is never a
credential for a resource route, on either service. It is long-lived and
high-value, so sending it on ordinary API calls would spread 90 days of
account access across logs, proxies, and crash reports, where a short-lived
access token would have leaked 15 minutes.

## Transport

- **`Authorization: Bearer <jwt>` for REST.** This is the only accepted
  credential for every state-changing method and every route other than the
  two below.
- **Cookie for `/mux` (the terminal WebSocket) and for `GET /api/v1/events`
  (SSE).** Browsers cannot set headers on either: the WebSocket handshake has
  no header API, and `EventSource` has none either. Both therefore
  authenticate with the cookie, whose value is the same short-lived JWT rather
  than a shared secret.
- The cookie is **deliberately not** accepted on any other route, and never on
  a state-changing method. It is ambient, so widening it would leave the
  gateway's CORS origin check as the only CSRF defence. New browser-reachable
  routes that cannot send a header need an explicit decision here first, not a
  quiet addition.

### The `/mux` and SSE cookie

- **Name: `ao_gw_token`.** Value: the current access token.
- Attributes: `Secure`, `HttpOnly`, `SameSite=None`, host-only (no `Domain`),
  `Path=/`.
- `SameSite=None` is required, not optional. The renderer runs on the
  `app://renderer` origin and talks to `https://vm.example.com`, which is a
  cross-site context, so under the browser default (`Lax`) the cookie is not
  attached to the WebSocket handshake or the `EventSource` request at all and
  both fail 401 with nothing to see on the client. `SameSite=None` is only
  honoured together with `Secure`, which is why the pair is stated together.
- Installed by the Electron main process, which owns it: it must be refreshed
  whenever the access token is, since it carries the same 15-minute
  expiry as the token inside it.
- The gateway strips both `Authorization` and this cookie before proxying to
  the daemon, so the credential never reaches the daemon.

## Verification on the VM

- Signature against the cached JWKS, then `iss`, then `aud` equal to this
  machine's id, then `exp` with 60s skew tolerance, then `sub` equal to the
  single account id in the machine's allowlist. Signature first, claims after,
  and a missing `exp` is a rejection rather than "no expiry".
- `iss` and `aud` are compared byte for byte. The control plane strips any
  trailing slash from `PUBLIC_ORIGIN` so the `iss` it mints matches the value
  the gateway pins.
- JWKS cache: 1 hour, with stale-if-error so a brief control-plane outage does
  not disconnect working users.
- The JWKS publishes the active key plus the next-rotation key, so a verifier
  caches the next key before it ever signs anything.

## The executable form of this contract

The control plane and the VM gateway are separate Go modules that cannot
import each other, so nothing but a committed artifact can carry a real token
from one to the other. That artifact is the golden fixture pair in
[`backend/internal/vmgateway/testdata/`](../backend/internal/vmgateway/testdata):
a `jwks.json` produced by `keys.Manager.JWKS()` and an `access_token.jwt`
produced by `Issuer.IssueAccessToken`, plus a `foreign_token.jwt` signed by a
key that is deliberately not in the JWKS.

- `backend/internal/vmgateway/golden_test.go` parses that JWKS and verifies
  that token through the real verification path, and checks the token's `kid`
  resolves in the key set (verification alone would not catch a `kid` change,
  since an unrecognised `kid` falls back to trying every key).
- `controlplane/internal/keys/golden_test.go` asserts the published JWK field
  set and `kid` derivation still match the fixture.
- `controlplane/internal/tokens/golden_test.go` owns the generator.

If you change anything above (a claim name, `kid` derivation, base64 padding,
the JWKS `x` encoding, the published field set) those tests fail, which is
what they are for. Regenerate the fixtures deliberately, and review the diff:

```
cd controlplane && go test ./internal/tokens/ -run TestGoldenFixtures -update
```

`jwks.json` is byte stable across regenerations (the fixture keys are derived
from committed test-only seed phrases), so a diff in it is always a real
contract change. The `.jwt` files change every time, because `iat`, `exp`, and
`jti` are minted fresh.
