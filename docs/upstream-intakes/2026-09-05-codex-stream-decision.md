# Codex EventSource transport decision

Companion to the frozen v3 backend handoff. The owner integrated codex-stream-boundary.patch with the authoritative v3 source. It adds one gateway regression test file only. No runtime source, generated contract, migration, frontend, or tracked evidence document is changed. The v3 archive remains unchanged; this test is an explicit additional patch.

## Decision

Do not extend cookie authentication to GET `/api/v1/agents/codex/accounts/events` under the current preservation policy. This is conditional on current authorization, not a claim that a read-only account stream could never be offered remotely.

The existing `isCookieAuthStreamPath` explicitly permits future renderer EventSource additions, but cookie eligibility and route eligibility are separate. Upstream's LAN policy blocks the entire `/api/v1/agents/codex` family, including its account events. V3 preserves that local-account-control boundary at both hosted and pair gateways. A cookie exception alone would still return 404; a functional remote stream requires an explicit exception to the control-route denial as well.

The retained cookie surface consists of the existing /mux handshake plus exact GET event streams: `/api/v1/events`, `/api/v1/notifications/stream`, and `/api/v1/sessions/{nonemptySessionId}/workspace/events`. Origin is required and the gateway's existing CORS policy still applies. Other routes stay bearer-only or blocked. No prefix-wide cookie expansion is added.

`docs/desktop-login-contract.md` describes the original mux/events transport. Current frozen source and `TestGateway_EventSourceStreams_AcceptCookie` already add notifications and workspace streams. These additive precedents establish the transport mechanism; they do not authorize forwarding local-only account routes. The new account stream publishes account state and identity metadata, not raw credentials, but remains within that currently blocked route family.

## Tests

New TestIntakeCodexStreamDoesNotExpandCookieBoundary runs both real hosted and pair handlers. It covers the exact SSE path, trailing slash, child path, prefix neighbor, account list, POST to events, logout, and DELETE account. For each, it checks absent/invalid/valid cookies and allowed/missing/hostile Origin. The cookie never becomes an extracted credential; blocked requests never reach the fake daemon. Rejection is 404 for the denied route or 403 at the hostile-Origin boundary.

The focused race command also runs existing positive EventSource-cookie and negative cookie-boundary tests, plus the v3 account/install route-denial regression. Results are in verification-codex-stream.log.

## Coordination

Owner hosted-ao-90 and frontend worker hosted-ao-92 were informed that the proposed remote EventSource seam conflicts with the retained local-account boundary. Worker 92 was asked to gate/degrade remote account controls unless the owner explicitly authorizes a policy exception. No owner-worktree or frontend edits were made.

If the owner later authorizes the exact read-only remote stream, implementation must jointly handle a method-specific route-denial exception, an exact GET-only cookie allowance, credential/Origin validation, a compatible route-policy representation, and negative mutation/neighbor tests. No such authorization is inferred from choosing EventSource withCredentials.
