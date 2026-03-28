---
"@composio/ao-web": patch
---

Fix runtime terminal websocket connectivity for npm-installed/prebuilt runs and harden project validation across API routes.

- add runtime terminal config endpoint (`/api/runtime/terminal`) so the browser can read runtime-selected ports
- make direct terminal client resolve websocket target from runtime config before connect/reconnect
- add AbortController (1.5s) to runtime config fetch so a slow endpoint cannot block WebSocket connection
- prevent repeated runtime config fetches on reconnect when the endpoint is unavailable
- centralize project existence check via `validateConfiguredProject` (uses `Object.hasOwn` to avoid prototype-chain bypass)
- apply semantic project validation to `/api/spawn`, `/api/issues`, `/api/verify`, and `/api/orchestrators`
- return deterministic `404 Unknown project` from all routes for non-configured project IDs
- normalize dashboard project filter to configured project IDs to prevent invalid query state propagation
