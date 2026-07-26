# Hosted AO Phase 1: remote daemon connection

## Goal

Prove that the existing AO desktop app can operate a daemon running on an
enterprise-hosted VM. The test deployment is reachable at
`https://api.ao.agentlab.in`.

This is a transport and deployment slice, not an enterprise account system. It
must support the dashboard's REST calls, live SSE streams, and the interactive
terminal mux.

## Decisions

- The AO daemon remains bound to loopback on the VM. Its HTTP router and Go API
  are unchanged in this phase.
- Caddy is the only public listener. It terminates TLS for
  `api.ao.agentlab.in` and reverse-proxies to the daemon's loopback port.
- Caddy requires a single high-entropy pairing secret, carried in the exact
  `ao_hosted_pair` Secure, HttpOnly cookie. Caddy compares it with the secret
  supplied to its service configuration; requests without an exact match are
  rejected before reaching AO.
- Phase 1 supplies the remote URL and pairing secret through Electron launch
  environment variables: `AO_REMOTE_URL` and `AO_REMOTE_TOKEN`.
- Electron's main process installs the cookie for the remote origin. The
  renderer never receives the pairing secret.
- In remote mode, the desktop does not launch, discover, supervise, restart,
  or stop a local daemon. It reports the configured remote target as ready.
- REST fetches and EventSource requests include credentials. The browser's
  WebSocket handshake carries the same cookie to `/mux`.
- When no remote URL is configured, all existing local-daemon behavior remains
  the default and unchanged.

## Architecture

```text
Electron main process
  AO_REMOTE_URL + AO_REMOTE_TOKEN
  └─ installs Secure, HttpOnly pairing cookie for api.ao.agentlab.in
       │
React renderer
  ├─ REST /api/v1/* (credentialed fetch)
  ├─ SSE /api/v1/events and notification stream (credentialed EventSource)
  └─ WSS /mux (browser cookie on upgrade)
       │
       ▼
Caddy on the hosted VM, publicly reachable on 443
  ├─ automatic TLS for api.ao.agentlab.in
  ├─ validates pairing cookie
  └─ reverse-proxies HTTP, SSE, and WebSocket traffic
       │
       ▼
Existing AO daemon on its configured 127.0.0.1 port
```

The desktop renderer uses one configured API base URL. Existing mux and SSE
code already derive their endpoints from that URL, so remote mode changes the
base selection and credential behavior rather than introducing another client
protocol. The proxy preserves the renderer Origin header; the daemon's existing
`app://renderer` CORS allowlist continues to authorize the credentialed desktop
requests.

## Security posture

This phase has no accounts or login screen. The pairing secret is the sole
authorization boundary and permits the full existing AO API, including session
and terminal actions. It must be a high-entropy value and must not appear in a
URL, renderer JavaScript, application logs, or source control.

TLS and Caddy enforce the public boundary. Azure exposes only ports 80 and 443
for certificate issuance and HTTPS; the AO daemon port remains inaccessible
from outside the VM.

The environment-variable secret is intentionally limited to this test slice.
A subsequent pairing UX will store the secret in macOS Keychain and provide a
connection settings screen without changing the proxy contract.

## Scope

Included:

- A Caddy configuration for the public TLS endpoint, secure-cookie validation,
  SSE streaming, and WebSocket upgrades.
- Electron remote mode driven by `AO_REMOTE_URL` and `AO_REMOTE_TOKEN`.
- Credentialed REST, SSE, notifications, and terminal mux transport.
- Unit coverage for remote/local mode selection and credentialed transports.
- A VM smoke test that verifies dashboard REST, an SSE connection, and a mux
  WebSocket attachment through `api.ao.agentlab.in`.

Excluded:

- Changes to the Go daemon listener, router, API, storage, CLI, or mobile
  bridge.
- Accounts, SSO, user management, tenant isolation, token rotation, and a
  desktop connection-settings UI.
- Remote browser-preview routing. A preview server started on the VM may be
  useful to an agent but is not reachable in the Mac's embedded browser without
  a separate design.
- Windows-specific remote-runtime work beyond the existing daemon behavior.

## Failure behavior

- An invalid or absent pairing cookie produces a proxy authentication failure;
  AO receives no request.
- TLS, DNS, or proxy outages surface as remote connection failures in the
  desktop app. They must never trigger local-daemon spawning in remote mode.
- If an SSE or mux connection drops, existing reconnect behavior reuses the
  configured remote base and the browser resends the pairing cookie.
- Local mode continues to use its existing daemon status and port discovery
  path.

## Acceptance criteria

1. The VM runs the existing AO daemon on loopback and Caddy serves
   `https://api.ao.agentlab.in` with a valid certificate.
2. Launching Electron with the two remote environment variables shows real VM
   projects and sessions.
3. Dashboard changes on the VM invalidate the desktop view through SSE.
4. The desktop attaches to and exchanges terminal data with a VM session over
   WSS `/mux`.
5. A request without the pairing cookie cannot reach AO through the public
   endpoint.
6. Launching without `AO_REMOTE_URL` keeps the current local experience intact.
