# Hosted Remote Daemon Phase One Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the Electron desktop operate a loopback-only AO daemon on a hosted VM through `https://api.ao.agentlab.in`, including REST, live SSE, and the `/mux` terminal WebSocket.

**Architecture:** AO remains unchanged and binds only `127.0.0.1:3001` on the VM. Caddy terminates TLS, validates one pairing cookie, and proxies HTTP, SSE, and WebSocket traffic. Electron reads the remote URL and secret only from its launch environment; main installs the HttpOnly cookie and reports a remote-ready daemon status, while the renderer uses that status base URL and includes credentials on cross-origin REST/SSE traffic.

**Tech Stack:** Electron, TypeScript, Vite, Vitest, Chromium session cookies, Caddy, existing Go AO daemon.

## Global Constraints

- Do not change Go daemon listener, router, API, CLI, storage, mobile bridge, or generated API files.
- AO remains bound to `127.0.0.1`; Caddy is the only public process.
- Public DNS target is `https://api.ao.agentlab.in`; Caddy exposes ports 80 and 443 only.
- `AO_REMOTE_URL` must be an HTTPS origin with no path, query, fragment, or embedded credentials.
- `AO_REMOTE_TOKEN` must be a non-empty URL-safe base64 token; never pass it to the renderer, put it in a URL, log it, commit it, or add it to telemetry.
- Pairing cookie name is `ao_hosted_pair`, with `Secure`, `HttpOnly`, `SameSite=None`, and `Path=/` attributes.
- Local daemon behavior is the default and must remain unchanged when `AO_REMOTE_URL` is absent.
- Remote browser-preview routing is out of scope.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `frontend/src/shared/remote-daemon.ts` | Parse and validate the two non-secret remote-mode inputs, and define the shared remote URL/cookie constants. |
| `frontend/src/shared/remote-daemon.test.ts` | Unit-test environment parsing and validation without Electron. |
| `frontend/src/main/remote-daemon.ts` | Install the pairing cookie into Electron's default session and create the non-secret remote-ready status. |
| `frontend/src/main/remote-daemon.test.ts` | Test cookie attributes and main-process remote status without launching Electron. |
| `frontend/src/shared/daemon-status.ts` | Add an optional non-secret `baseUrl` field to the supervisor-to-renderer status payload. |
| `frontend/src/main.ts` | Select remote mode before daemon lifecycle work; do not spawn, attach, or stop a local process in that mode. |
| `frontend/src/renderer/lib/daemon-status.ts` | Prefer a remote status `baseUrl` over a loopback port. |
| `frontend/src/renderer/lib/api-client.ts` | Send cross-origin remote API requests with `credentials: "include"`. |
| `frontend/src/renderer/lib/event-transport.ts` | Build the CDC EventSource with credentials for the remote target. |
| `frontend/src/renderer/lib/notifications.ts` | Build the notifications EventSource with credentials for the remote target. |
| `frontend/src/renderer/lib/*.test.ts` | Extend the existing API/status/SSE tests for remote behavior while retaining local assertions. |
| `deploy/hosted/Caddyfile` | TLS reverse proxy and exact pairing-cookie gate for the VM. |
| `deploy/hosted/README.md` | Operator setup, secret generation, firewall/DNS requirements, and the VM-to-desktop smoke test. |

## Task 1: Define and test the remote-mode contract

**Files:**
- Create: `frontend/src/shared/remote-daemon.ts`
- Create: `frontend/src/shared/remote-daemon.test.ts`
- Modify: `frontend/src/shared/daemon-status.ts`

**Interfaces:**
- Produces `REMOTE_PAIRING_COOKIE_NAME = "ao_hosted_pair"`.
- Produces `RemoteDaemonConfig = { baseUrl: string; token: string }` for the Electron main process only.
- Produces `readRemoteDaemonConfig(env: Record<string, string | undefined>): RemoteDaemonConfig | null`.
- Produces `isRemoteDaemonBaseUrl(baseUrl: string): boolean` for renderer transport selection.
- Extends `DaemonStatus` with `baseUrl?: string`; this is an HTTPS origin and never contains a token.

- [ ] **Step 1: Write the failing parser tests**

```ts
import { describe, expect, it } from "vitest";
import { readRemoteDaemonConfig } from "./remote-daemon";

describe("readRemoteDaemonConfig", () => {
	it("returns a normalized remote config for an HTTPS origin and URL-safe token", () => {
		expect(readRemoteDaemonConfig({
			AO_REMOTE_URL: "https://api.ao.agentlab.in/",
			AO_REMOTE_TOKEN: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		})).toEqual({
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		});
	});

	it("rejects a partial remote configuration without returning a local fallback", () => {
		expect(() => readRemoteDaemonConfig({ AO_REMOTE_URL: "https://api.ao.agentlab.in" }))
			.toThrow("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	});
});
```

- [ ] **Step 2: Run the parser test to verify it fails**

Run: `cd frontend && npm test -- src/shared/remote-daemon.test.ts`

Expected: FAIL because `./remote-daemon` does not exist.

- [ ] **Step 3: Implement the pure contract**

```ts
export const REMOTE_PAIRING_COOKIE_NAME = "ao_hosted_pair";

export type RemoteDaemonConfig = { baseUrl: string; token: string };

export function readRemoteDaemonConfig(env: Record<string, string | undefined>): RemoteDaemonConfig | null {
	const rawURL = env.AO_REMOTE_URL?.trim() ?? "";
	const token = env.AO_REMOTE_TOKEN?.trim() ?? "";
	if (!rawURL && !token) return null;
	if (!rawURL || !token) throw new Error("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	if (!/^[A-Za-z0-9_-]+$/.test(token)) throw new Error("AO_REMOTE_TOKEN must be URL-safe base64");
	const url = new URL(rawURL);
	if (url.protocol !== "https:" || url.pathname !== "/" || url.search || url.hash || url.username || url.password) {
		throw new Error("AO_REMOTE_URL must be an HTTPS origin without a path, query, fragment, or credentials");
	}
	return { baseUrl: url.origin, token };
}

export function isRemoteDaemonBaseUrl(baseUrl: string): boolean {
	return baseUrl.startsWith("https://");
}
```

Add `baseUrl?: string` to `DaemonStatus` beside the existing `port?: number` field, documenting that it is non-secret and used only for an externally hosted daemon.

- [ ] **Step 4: Add the complete validation matrix**

Add assertions for: neither variable set returns `null`; token-only input throws; `http://`, a URL path, query string, fragment, and credentials each throw; tokens containing a space or slash throw; and `isRemoteDaemonBaseUrl("https://api.ao.agentlab.in")` is true while loopback HTTP is false.

- [ ] **Step 5: Run the focused tests**

Run: `cd frontend && npm test -- src/shared/remote-daemon.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit the contract**

```bash
git add frontend/src/shared/remote-daemon.ts frontend/src/shared/remote-daemon.test.ts frontend/src/shared/daemon-status.ts
git commit -m "feat: define remote daemon configuration"
```

## Task 2: Configure pairing and remote-ready status in Electron main

**Files:**
- Create: `frontend/src/main/remote-daemon.ts`
- Create: `frontend/src/main/remote-daemon.test.ts`
- Modify: `frontend/src/main.ts`
- Modify: `frontend/src/preload.ts` only if TypeScript inference needs the existing daemon status bridge re-exported after `DaemonStatus` changes

**Interfaces:**
- Consumes `RemoteDaemonConfig`, `REMOTE_PAIRING_COOKIE_NAME`, and `DaemonStatus` from Task 1.
- Produces `installRemoteDaemonCookie(cookieStore, config): Promise<void>`.
- Produces `remoteDaemonReadyStatus(config): DaemonStatus` with `{ state: "ready", baseUrl: config.baseUrl }`.
- `main.ts` stores `remoteDaemonConfig: RemoteDaemonConfig | null` exactly once at startup and keeps its secret in main-process memory only.

- [ ] **Step 1: Write the failing main-process unit tests**

```ts
import { describe, expect, it, vi } from "vitest";
import { installRemoteDaemonCookie, remoteDaemonReadyStatus } from "./remote-daemon";

it("installs the pairing secret as a secure HttpOnly remote-origin cookie", async () => {
	const set = vi.fn().mockResolvedValue(undefined);
	await installRemoteDaemonCookie({ set }, {
		baseUrl: "https://api.ao.agentlab.in",
		token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
	});
	expect(set).toHaveBeenCalledWith(expect.objectContaining({
		url: "https://api.ao.agentlab.in",
		name: "ao_hosted_pair",
		httpOnly: true,
		secure: true,
		sameSite: "no_restriction",
		path: "/",
	}));
});

it("returns a ready status without exposing the pairing token", () => {
	expect(remoteDaemonReadyStatus({ baseUrl: "https://api.ao.agentlab.in", token: "secret" }))
		.toEqual({ state: "ready", baseUrl: "https://api.ao.agentlab.in", message: "Connected to remote daemon" });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npm test -- src/main/remote-daemon.test.ts`

Expected: FAIL because `./remote-daemon` does not exist.

- [ ] **Step 3: Implement the Electron-independent main helper**

```ts
type CookieStore = { set: (details: Electron.CookiesSetDetails) => Promise<void> };

export async function installRemoteDaemonCookie(store: CookieStore, config: RemoteDaemonConfig): Promise<void> {
	await store.set({
		url: config.baseUrl,
		name: REMOTE_PAIRING_COOKIE_NAME,
		value: config.token,
		domain: new URL(config.baseUrl).hostname,
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "no_restriction",
	});
}

export function remoteDaemonReadyStatus(config: RemoteDaemonConfig): DaemonStatus {
	return { state: "ready", baseUrl: config.baseUrl, message: "Connected to remote daemon" };
}
```

- [ ] **Step 4: Wire remote mode before local lifecycle work**

In `frontend/src/main.ts`:

1. Read `readRemoteDaemonConfig(process.env)` once during module initialization. Convert an invalid configuration into a stored `DaemonStatus` error with code `not_configured`; do not log the token.
2. In the `app.whenReady()` startup block, before `createWindow()`, call `installRemoteDaemonCookie(session.defaultSession.cookies, remoteDaemonConfig)` when configuration is valid. On failure, set an error status and do not spawn a local daemon.
3. Make `refreshDaemonStatus()` return `remoteDaemonReadyStatus(remoteDaemonConfig)` in valid remote mode.
4. Make `startDaemon()` return that remote-ready status without calling `ensureShellEnv`, `resolveDaemonLaunch`, `inspectExistingDaemon`, or `spawn`.
5. Make `stopDaemon()` return the same remote-ready status without removing the cookie or contacting the VM; remote mode must not gain a remote shutdown control.
6. Replace the unconditional `void startDaemon()` in `app.whenReady()` with the existing call, relying on the new early remote-mode return.

Keep all current local branches unchanged and ensure `setDaemonStatus` receives the non-secret ready status so the existing IPC subscriber updates the renderer.

- [ ] **Step 5: Extend the main-helper failure tests**

Add one test whose `set` mock rejects with `new Error("cookie store unavailable")` and assert `installRemoteDaemonCookie(...)` rejects with that error. Add one test proving `remoteDaemonReadyStatus(...)` omits the `token` field even when the supplied config has one. In `main.ts`, keep the remote-mode branches before every local lifecycle call named in Step 4; this is the static boundary that prevents fallback spawning.

- [ ] **Step 6: Run focused tests and typecheck**

Run:

```bash
cd frontend && npm test -- src/main/remote-daemon.test.ts src/shared/remote-daemon.test.ts
npm run typecheck
```

Expected: PASS.

- [ ] **Step 7: Commit Electron remote mode**

```bash
git add frontend/src/main/remote-daemon.ts frontend/src/main/remote-daemon.test.ts frontend/src/main.ts frontend/src/preload.ts
git commit -m "feat: add Electron remote daemon mode"
```

## Task 3: Make renderer REST and SSE transports credential-aware

**Files:**
- Modify: `frontend/src/renderer/lib/daemon-status.ts`
- Modify: `frontend/src/renderer/lib/api-client.ts`
- Modify: `frontend/src/renderer/lib/event-transport.ts`
- Modify: `frontend/src/renderer/lib/notifications.ts`
- Modify: `frontend/src/renderer/lib/api-client.test.ts`
- Modify: `frontend/src/renderer/lib/event-transport.test.ts`
- Modify: `frontend/src/renderer/lib/notifications.test.ts`

**Interfaces:**
- Consumes `DaemonStatus.baseUrl` and `isRemoteDaemonBaseUrl` from Tasks 1–2.
- `applyDaemonStatus(nextStatus)` selects `nextStatus.baseUrl` before a loopback `nextStatus.port`.
- Remote REST requests are sent with `credentials: "include"`.
- Remote EventSource instances are constructed with `{ withCredentials: true }`.
- Local EventSource and fetch behavior keeps its current credentials settings.

- [ ] **Step 1: Write the failing renderer tests**

Add these assertions:

```ts
it("rebases remote API calls and includes the pairing cookie", async () => {
	setApiBaseUrl("https://api.ao.agentlab.in");
	await apiClient.GET("/api/v1/projects");
	expect(fetch).toHaveBeenCalledWith(
		"https://api.ao.agentlab.in/api/v1/projects",
		expect.objectContaining({ credentials: "include" }),
	);
});

it("opens the remote CDC stream with credentials", () => {
	getApiBaseUrlMock.mockReturnValue("https://api.ao.agentlab.in");
	createEventTransport(fakeQueryClient()).connect();
	expect(EventSourceStub.instances[0]).toMatchObject({
		url: "https://api.ao.agentlab.in/api/v1/events",
		options: { withCredentials: true },
	});
});
```

Update each EventSource test stub constructor to retain its optional second argument:

```ts
constructor(url: string, readonly options?: EventSourceInit) {
	this.url = url;
	EventSourceStub.instances.push(this);
}
```

Also add a daemon-status test asserting that `{ state: "ready", baseUrl: "https://api.ao.agentlab.in", port: 3001 }` selects the HTTPS URL rather than loopback.

- [ ] **Step 2: Run the renderer tests to verify they fail**

Run:

```bash
cd frontend && npm test -- src/renderer/lib/api-client.test.ts src/renderer/lib/event-transport.test.ts src/renderer/lib/notifications.test.ts
```

Expected: FAIL because remote requests preserve the default `same-origin` credential mode and EventSource is called without options.

- [ ] **Step 3: Prefer remote status URLs in the renderer**

Implement the base choice in `applyDaemonStatus`:

```ts
if (nextStatus.state === "ready" && nextStatus.baseUrl) {
	setApiBaseUrl(nextStatus.baseUrl);
} else if (nextStatus.state === "ready" && nextStatus.port) {
	setApiBaseUrl(`http://127.0.0.1:${nextStatus.port}`);
} else {
	setApiBaseUrl(null);
}
```

- [ ] **Step 4: Include credentials only for the remote target**

In `runtimeFetch`, calculate credentials from the runtime base URL before calling `fetch`:

```ts
const credentials = isRemoteDaemonBaseUrl(baseUrl) ? "include" : input.credentials;
return fetch(target, {
	method: input.method,
	headers: input.headers,
	body,
	signal: input.signal,
	credentials,
	cache: input.cache,
	redirect: input.redirect,
	referrerPolicy: input.referrerPolicy,
	integrity: input.integrity,
	keepalive: input.keepalive,
});
```

In both `event-transport.ts` and `notifications.ts`, construct the source as:

```ts
new EventSource(streamURL, { withCredentials: isRemoteDaemonBaseUrl(baseUrl) });
```

Import the shared predicate rather than duplicating an HTTPS test. Do not modify `terminal-mux.ts`: its existing `muxUrlFromApiBase` maps `https://` to `wss://`, and browser WebSocket cookie handling is the behavior being exercised by the VM smoke test.

- [ ] **Step 5: Add local regression assertions**

Assert local HTTP EventSource instances receive `{ withCredentials: false }` and that local REST preserves the current request credentials. This ensures remote cookie support does not alter the loopback path.

- [ ] **Step 6: Run focused tests and typecheck**

Run:

```bash
cd frontend && npm test -- src/renderer/lib/api-client.test.ts src/renderer/lib/event-transport.test.ts src/renderer/lib/notifications.test.ts src/renderer/lib/terminal-mux.test.ts
npm run typecheck
```

Expected: PASS. The existing terminal mux URL test must continue to assert `https://api.ao.agentlab.in` maps to `wss://api.ao.agentlab.in/mux`.

- [ ] **Step 7: Commit the renderer transport work**

```bash
git add frontend/src/renderer/lib/daemon-status.ts frontend/src/renderer/lib/api-client.ts frontend/src/renderer/lib/event-transport.ts frontend/src/renderer/lib/notifications.ts frontend/src/renderer/lib/api-client.test.ts frontend/src/renderer/lib/event-transport.test.ts frontend/src/renderer/lib/notifications.test.ts
git commit -m "feat: authenticate remote dashboard transports"
```

## Task 4: Add the hosted VM proxy deployment assets

**Files:**
- Create: `deploy/hosted/Caddyfile`
- Create: `deploy/hosted/README.md`

**Interfaces:**
- Consumes VM environment variable `AO_HOSTED_PAIR_TOKEN`, containing the same URL-safe value passed locally as `AO_REMOTE_TOKEN`.
- Caddy accepts only requests whose `ao_hosted_pair` cookie exactly matches that token.
- Caddy proxies all accepted paths to `127.0.0.1:3001`; its built-in reverse proxy preserves SSE and automatically handles WebSocket upgrade.

- [ ] **Step 1: Write the Caddy configuration**

Create `deploy/hosted/Caddyfile` with this exact structure:

```caddyfile
{
	email ops@agentlab.in
}

api.ao.agentlab.in {
	@paired header_regexp pairing Cookie "(^|;\\s*)ao_hosted_pair={env.AO_HOSTED_PAIR_TOKEN}(;|$)"
	handle @paired {
		reverse_proxy 127.0.0.1:3001 {
			flush_interval -1
		}
	}

	respond "Unauthorized" 401
}
```

Keep the token URL-safe so it is literal-safe in this regular expression. Do not add a daemon public bind, a second daemon listener, or proxy paths for a browser preview server.

- [ ] **Step 2: Write the operator README**

Document these exact deployment steps:

```bash
# VM: create a URL-safe pairing secret once and store it in a root-readable env file.
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='

# VM: run the existing daemon only on loopback.
AO_PORT=3001 ao start

# VM: load Caddy with AO_HOSTED_PAIR_TOKEN exported by its systemd EnvironmentFile.
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy

# Mac: launch the desktop build in remote mode.
AO_REMOTE_URL=https://api.ao.agentlab.in AO_REMOTE_TOKEN="$AO_HOSTED_PAIR_TOKEN" npm run dev
```

Also require: an A record for `api.ao.agentlab.in` pointing to `YOUR_VM_PUBLIC_IP`; Azure NSG and host firewall allow TCP 80/443 only; port 3001 is not exposed; and Caddy must run with access to its certificate storage. Explain that `app://renderer` is already the Go daemon's default allowed CORS origin and must not be overwritten by `AO_ALLOWED_ORIGINS` on the VM.

- [ ] **Step 3: Document the manual end-to-end smoke test**

Add these checks, in order:

1. `curl -i https://api.ao.agentlab.in/healthz` returns `401` without the cookie.
2. `curl -i --cookie "ao_hosted_pair=$AO_HOSTED_PAIR_TOKEN" https://api.ao.agentlab.in/healthz` returns the daemon health response.
3. Start the local Electron app with the two remote variables and verify the sidebar displays a project stored on the VM.
4. Change a VM session through the AO API or CLI and verify the desktop board refreshes without a manual reload; this verifies the credentialed SSE connection.
5. Open that session's terminal in Electron, type `pwd`, and verify the output reports the VM workspace path; this verifies the WSS mux and pairing cookie on the upgrade request.
6. Quit the app, start it with no remote variables, and verify a local daemon is discovered or started as before.

- [ ] **Step 4: Validate configuration syntax without starting deployment services**

Run on the VM after copying the Caddyfile and exporting a non-secret test token in the service environment:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
```

Expected: Caddy reports the configuration is valid. Do not print the token in the terminal output.

- [ ] **Step 5: Commit deployment assets**

```bash
git add deploy/hosted/Caddyfile deploy/hosted/README.md
git commit -m "docs: add hosted AO proxy deployment"
```

## Task 5: Run the complete local and hosted verification gate

**Files:**
- Modify only if a failed verification reveals a scoped defect from Tasks 1–4.

**Interfaces:**
- Consumes the completed Electron remote mode and Caddy deployment assets.
- Produces verified local-mode regression coverage and a successful hosted VM smoke result.

- [ ] **Step 1: Run the frontend unit suite**

Run:

```bash
cd frontend && npm test
```

Expected: PASS.

- [ ] **Step 2: Run frontend typecheck and build**

Run:

```bash
cd frontend && npm run typecheck && npm run build
```

Expected: PASS.

- [ ] **Step 3: Run backend regression checks even though Go is unchanged**

Run:

```bash
cd backend && go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 4: Deploy only the committed Caddy configuration and run the hosted smoke test**

Follow `deploy/hosted/README.md` exactly. Record only command outcomes, hostnames, HTTP status codes, and the fact that terminal output originated on the VM; do not record the pairing token in Git, docs, test artifacts, or shell history.

- [ ] **Step 5: Verify the git diff contains no credentials or generated drift**

Run:

```bash
git status --short
git diff --check origin/main...HEAD
git grep -n 'AO_REMOTE_TOKEN=.*[A-Za-z0-9_-]\{20,\}' -- ':!docs/superpowers/plans/*' || true
```

Expected: no key files, tokens, local state, or generated API changes are staged.

- [ ] **Step 6: Confirm the verification gate did not create an unrelated change**

Run: `git status --short`

Expected: no new files other than the scoped Task 1–4 implementation files. If a test failure required a code change, return to the task that owns that file, rerun its focused test, and use that task's explicit commit command. Do not make a verification-only commit.

## Self-Review

- Spec coverage: Task 1 defines the non-secret remote contract; Task 2 installs the secret only in Electron main and prevents local lifecycle behavior; Task 3 covers REST, CDC SSE, notifications SSE, and preserves existing WSS URL derivation; Task 4 covers TLS, exact cookie validation, DNS, firewall, and VM operation; Task 5 covers local regressions and the three required hosted transports.
- Scope: no task changes the Go daemon, network bind, API, CLI, account model, mobile bridge, Keychain persistence, or preview routing.
- Consistency: the pairing cookie name is `ao_hosted_pair` everywhere; remote origin is always `https://api.ao.agentlab.in`; remote status uses `DaemonStatus.baseUrl`; the token remains main-process/Caddy-only.
