import createClient from "openapi-fetch";
import type { paths } from "../../api/schema";
import type { DaemonStatus } from "../../shared/daemon-status";
import { isRemoteDaemonBaseUrl } from "../../shared/remote-daemon";
import { aoBridge } from "./bridge";
import { daemonFailureMessage } from "./daemon-failure";
import { captureRendererEvent } from "./telemetry";

function devApiBaseUrl(): string {
	return typeof window === "undefined" ? "http://127.0.0.1:3001" : window.location.origin;
}

const explicitApiBaseUrl = import.meta.env.VITE_AO_API_BASE_URL;
const initialApiBaseUrl = explicitApiBaseUrl ?? (import.meta.env.DEV ? devApiBaseUrl() : "http://127.0.0.1:3001");

let runtimeApiBaseUrl: string | null = explicitApiBaseUrl ?? null;
let daemonStatus: DaemonStatus = { state: "stopped" };

const baseUrlListeners = new Set<() => void>();

export function getApiBaseUrl(): string {
	return runtimeApiBaseUrl ?? "";
}

export function hasTrustedApiBaseUrl(): boolean {
	return runtimeApiBaseUrl !== null;
}

/**
 * Subscribe to base-URL changes (useSyncExternalStore-compatible). Long-lived
 * connections bound to a specific port — the terminal mux WebSocket, the SSE
 * stream — use this to rebind when the daemon comes back on a different port.
 */
export function subscribeApiBaseUrl(listener: () => void): () => void {
	baseUrlListeners.add(listener);
	return () => {
		baseUrlListeners.delete(listener);
	};
}

export function setApiBaseUrl(nextBaseUrl: string | null): void {
	const normalized = (nextBaseUrl ?? explicitApiBaseUrl ?? null)?.replace(/\/+$/, "") ?? null;
	if (normalized === runtimeApiBaseUrl) return;
	runtimeApiBaseUrl = normalized;
	baseUrlListeners.forEach((listener) => listener());
}

// The renderer records every supervisor status here so API requests made while
// no daemon URL is trusted can return the actual startup failure, not a generic
// availability message.
export function setApiDaemonStatus(nextStatus: DaemonStatus): void {
	daemonStatus = nextStatus;
}

// Route templates from the generated OpenAPI schema (frontend/src/api/schema.ts).
// Operation strings sent to telemetry must never contain raw IDs (project IDs
// are user-chosen strings), so we match each request path against these
// templates and report the template — collapsing `{param}` to `:id` — rather
// than guessing which segments are identifiers. Matching from the schema keeps
// static child routes (notifications/read-all, sessions/cleanup) intact and
// still normalizes IDs for every resource, including ones a segment heuristic
// would miss (orchestrators/{id}). Keep in sync with schema.ts.
const ROUTE_TEMPLATES = [
	"/api/v1/agents",
	"/api/v1/agents/refresh",
	"/api/v1/agents/{agent}/models",
	"/api/v1/agents/{agent}/models/refresh",
	"/api/v1/agents/{agent}/probe",
	"/api/v1/events",
	"/api/v1/import",
	"/api/v1/notifications",
	"/api/v1/notifications/{id}",
	"/api/v1/notifications/read-all",
	"/api/v1/notifications/stream",
	"/api/v1/orchestrators",
	"/api/v1/orchestrators/{id}",
	"/api/v1/projects",
	"/api/v1/projects/clone",
	"/api/v1/projects/initialize",
	"/api/v1/projects/{id}",
	"/api/v1/projects/{id}/config",
	"/api/v1/prs/{id}/merge",
	"/api/v1/prs/{id}/resolve-comments",
	"/api/v1/sessions",
	"/api/v1/sessions/{sessionId}",
	"/api/v1/sessions/{sessionId}/activity",
	"/api/v1/sessions/{sessionId}/agent-switches",
	"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/handoff",
	"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover",
	"/api/v1/sessions/{sessionId}/interface-transition",
	"/api/v1/sessions/{sessionId}/kill",
	"/api/v1/sessions/{sessionId}/pr",
	"/api/v1/sessions/{sessionId}/pr/claim",
	"/api/v1/sessions/{sessionId}/preview",
	"/api/v1/sessions/{sessionId}/preview/files/*",
	"/api/v1/sessions/{sessionId}/preview/server",
	"/api/v1/sessions/{sessionId}/resume-agent",
	"/api/v1/sessions/{sessionId}/restore",
	"/api/v1/sessions/{sessionId}/switch-agent",
	"/api/v1/sessions/{sessionId}/reviews",
	"/api/v1/sessions/{sessionId}/reviews/cancel",
	"/api/v1/sessions/{sessionId}/reviews/comments/resolve",
	"/api/v1/sessions/{sessionId}/reviews/submit",
	"/api/v1/sessions/{sessionId}/reviews/trigger",
	"/api/v1/sessions/{sessionId}/rollback",
	"/api/v1/sessions/{sessionId}/send",
	"/api/v1/sessions/{sessionId}/workspace/events",
	"/api/v1/sessions/{sessionId}/workspace/file",
	"/api/v1/sessions/{sessionId}/workspace/files",
	"/api/v1/sessions/cleanup",
] as const;

// Resource collections whose next path segment is an identifier. Only used as a
// defensive fallback for paths not covered by ROUTE_TEMPLATES; keeps IDs out of
// telemetry for known collections even if a route is ever missed above.
const RESOURCE_SEGMENTS = new Set([
	"agents",
	"projects",
	"sessions",
	"notifications",
	"workspaces",
	"prs",
	"orchestrators",
]);

// Match a path against one template. `{param}` matches any single segment
// (reported as `:id`), a trailing `*` matches the remaining path, and every
// other segment must match literally. Returns the normalized template plus a
// score = number of literal segments matched, so the most specific template
// wins when several match (e.g. `read-all` beats `{id}`).
function matchRouteTemplate(pathname: string, template: string): { normalized: string; score: number } | null {
	const pathSegs = pathname.split("/");
	const tmplSegs = template.split("/");
	const out: string[] = [];
	let score = 0;
	for (let i = 0; i < tmplSegs.length; i += 1) {
		const t = tmplSegs[i];
		if (t === "*") {
			out.push("*");
			return { normalized: out.join("/"), score };
		}
		const p = pathSegs[i];
		if (p === undefined) return null;
		if (t.startsWith("{") && t.endsWith("}")) {
			out.push(":id");
		} else if (t === p) {
			out.push(t);
			score += 1;
		} else {
			return null;
		}
	}
	if (pathSegs.length !== tmplSegs.length) return null;
	return { normalized: out.join("/"), score };
}

function fallbackNormalize(pathname: string): string {
	const segments = pathname.split("/");
	for (let i = 0; i < segments.length - 1; i += 1) {
		if (RESOURCE_SEGMENTS.has(segments[i]) && segments[i + 1]) {
			segments[i + 1] = ":id";
			i += 1;
		}
	}
	return segments.join("/");
}

export function normalizeApiOperation(method: string, pathname: string): string {
	let best: { normalized: string; score: number } | null = null;
	for (const template of ROUTE_TEMPLATES) {
		const match = matchRouteTemplate(pathname, template);
		if (match && (best === null || match.score > best.score)) best = match;
	}
	return `${method.toUpperCase()} ${best?.normalized ?? fallbackNormalize(pathname)}`;
}

type ApiErrorCategory =
	| "daemon_unavailable"
	| "machine_token_unavailable"
	| "network_error"
	| "http_4xx"
	| "http_5xx";

// One event per (operation, category, status) per window: a daemon outage
// makes every polling query fail at once and on every retry — the failure
// signal matters, the storm does not.
const API_ERROR_DEDUPE_MS = 30_000;
const lastApiErrorAt = new Map<string, number>();

function reportApiError(operation: string, category: ApiErrorCategory, status?: number): void {
	const key = `${operation}|${category}|${status ?? ""}`;
	const now = Date.now();
	const last = lastApiErrorAt.get(key);
	if (last !== undefined && now - last < API_ERROR_DEDUPE_MS) return;
	lastApiErrorAt.set(key, now);
	void captureRendererEvent("ao.renderer.api_error", {
		operation,
		error_category: category,
		status,
	});
}

// Backstop against a blackholed connection (dead gateway, network that changed
// mid-request), not a latency target: the daemon's own REST handler timeout is
// 60s (backend/internal/config: DefaultRequestTimeout), and every route this
// client calls is a bounded request/response, never a stream — SSE
// (/api/v1/events, /api/v1/notifications/stream) and the session workspace
// event stream open their own EventSource directly against the base URL, and
// the terminal mux is a WebSocket; neither goes through runtimeFetch. A margin
// above the daemon's own timeout lets the daemon's timeout response win the race.
const REQUEST_TIMEOUT_MS = 65_000;

/** Rejects with `signal`'s abort reason as soon as it fires, whichever settles first. */
function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
	if (signal.aborted) return Promise.reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
	return new Promise<T>((resolve, reject) => {
		const onAbort = () => reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
		signal.addEventListener("abort", onAbort, { once: true });
		promise.then(
			(value) => {
				signal.removeEventListener("abort", onAbort);
				resolve(value);
			},
			(error: unknown) => {
				signal.removeEventListener("abort", onAbort);
				reject(error);
			},
		);
	});
}

async function runtimeFetch(input: Request): Promise<Response> {
	const operation = normalizeApiOperation(input.method, new URL(input.url).pathname);
	const baseUrl = runtimeApiBaseUrl;
	if (baseUrl === null) {
		reportApiError(operation, "daemon_unavailable", 503);
		return new Response(JSON.stringify({ message: daemonFailureMessage(daemonStatus), code: daemonStatus.code }), {
			status: 503,
			headers: { "Content-Type": "application/json" },
		});
	}

	// Bearer is the only credential the machine gateway accepts on a REST route:
	// its cookie is confined to /mux and the SSE stream, so nothing
	// state-changing rides an ambient credential (controlplane/TOKEN_CONTRACT.md).
	// Asked for per request because the main process owns expiry and the silent
	// refresh. Null when no machine is selected (local daemon) or in browser preview.
	const remote = baseUrl ? isRemoteDaemonBaseUrl(baseUrl) : false;
	const gatewayToken = remote ? await aoBridge.machines.gatewayToken() : null;
	if (remote && !gatewayToken) {
		// No usable machine credential right now (the silent refresh is failing, or
		// the main process no longer has this machine). A request sent anyway would
		// reach the gateway bare and become a silent 401 the gateway does not even
		// log; answer locally instead so the failure is visible and retryable.
		reportApiError(operation, "machine_token_unavailable", 503);
		return new Response(
			JSON.stringify({
				message: "This machine's sign-in is not available right now. Reconnecting.",
				code: "machine_token_unavailable",
			}),
			{ status: 503, headers: { "Content-Type": "application/json" } },
		);
	}

	const url = new URL(input.url);
	const target = baseUrl ? new URL(url.pathname + url.search + url.hash, baseUrl) : null;
	const credentials = remote ? "include" : input.credentials;
	// Bearer is only ever added on the rebase path below, so passthrough (send
	// input untouched) requires no token to attach — which a remote call always
	// has by this point, so remote never takes this branch.
	const passthrough = !baseUrl || (target!.href === input.url && credentials === input.credentials && !gatewayToken);

	// Buffer the body once, outside the send closure: a Request's body stream
	// can only be read once, and a 401/403 retry below calls send() a second
	// time with the same body. `new Request(target, input)` (source Request as
	// the *init* argument) reads input's `duplex` getter, which Electron's
	// Chromium lacks and throws "The duplex member must be specified" for any
	// request with a body — buffering to an ArrayBuffer up front sidesteps that
	// streaming-duplex path entirely, on both the first send and the retry.
	const body =
		passthrough || input.method === "GET" || input.method === "HEAD" ? undefined : await input.arrayBuffer();

	// Composed, not replacing: a caller-supplied signal (react-query's, once a
	// hook forwards it) must still cancel the request the moment the caller no
	// longer wants it, same as before this timeout existed.
	const signal = AbortSignal.any([input.signal, AbortSignal.timeout(REQUEST_TIMEOUT_MS)]);

	const send = async (token: string | null): Promise<Response> => {
		if (passthrough) {
			// input's own signal is left untouched here (see the duplex note above:
			// even `{ signal }` as a second init argument next to a Request risks the
			// same body-getter path on some engines), so this call cannot truly abort
			// the underlying connection on timeout. raceAbort still rejects the
			// promise the caller is awaiting, which is what turns a hang into a
			// visible, retryable error; the abandoned fetch is left to fail or settle
			// on its own.
			return raceAbort(fetch(input), signal);
		}
		const headers = new Headers(input.headers);
		if (token) headers.set("Authorization", `Bearer ${token}`);
		return fetch(target!, {
			method: input.method,
			headers,
			body,
			signal,
			credentials,
			cache: input.cache,
			redirect: input.redirect,
			referrerPolicy: input.referrerPolicy,
			integrity: input.integrity,
			keepalive: input.keepalive,
		});
	};

	const sendReportingFailure = async (token: string | null): Promise<Response> => {
		try {
			return await send(token);
		} catch (error) {
			// Caller-initiated aborts (unmounted components cancelling queries) are
			// not failures and stay unreported, same as before this timeout existed.
			// AbortSignal.timeout()'s own rejection is a "TimeoutError" DOMException,
			// not "AbortError", so this function's own timeout firing still falls
			// through and is reported — a hang must surface as a real network error,
			// not be swallowed like a benign cancellation.
			if (!(error instanceof DOMException && error.name === "AbortError")) {
				reportApiError(operation, "network_error");
			}
			throw error;
		}
	};

	let response = await sendReportingFailure(gatewayToken);

	// A 401/403 from the gateway means the bearer this request carried is no
	// good, even though it looked unexpired client-side (server-side
	// revocation, key rotation, or clock skew). Re-mint once and retry once; a
	// failed re-mint falls back to the same local machine_token_unavailable
	// path as the up-front no-token gate above, rather than a bare retry. At
	// most one retry per request: the second attempt's response is returned
	// as-is, 401/403 included, with no further re-mint.
	if (remote && (response.status === 401 || response.status === 403)) {
		const refreshed = await aoBridge.machines.gatewayToken(true);
		if (!refreshed) {
			reportApiError(operation, "machine_token_unavailable", 503);
			return new Response(
				JSON.stringify({
					message: "This machine's sign-in is not available right now. Reconnecting.",
					code: "machine_token_unavailable",
				}),
				{ status: 503, headers: { "Content-Type": "application/json" } },
			);
		}
		response = await sendReportingFailure(refreshed);
	}

	if (!response.ok) {
		reportApiError(operation, response.status >= 500 ? "http_5xx" : "http_4xx", response.status);
	}
	return response;
}

export const apiClient = createClient<paths>({
	baseUrl: initialApiBaseUrl,
	fetch: runtimeFetch,
});

/**
 * Human-readable message from an openapi-fetch `error` value. The daemon's
 * error body is `{ error, code, message, requestId }` (backend apierr) — a
 * plain object, so `String(error)` renders "[object Object]". Falls back
 * through Error instances and strings.
 */
export function apiErrorCode(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown };
		if (typeof body.code === "string" && body.code !== "") return body.code;
	}
	return undefined;
}

/** Correlation id from the daemon's stable error envelope. */
export function apiErrorRequestId(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { requestId?: unknown };
		if (typeof body.requestId === "string" && body.requestId !== "") return body.requestId;
	}
	return undefined;
}

export function apiErrorMessage(error: unknown, fallback = "Request failed"): string {
	if (error instanceof Error) return error.message;
	if (typeof error === "string" && error !== "") return error;
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown; message?: unknown; error?: unknown };
		if (typeof body.error === "object" && body.error !== null) {
			return apiErrorMessage(body.error, fallback);
		}
		const code = typeof body.code === "string" && body.code !== "" ? body.code : "";
		if (typeof body.message === "string" && body.message !== "") {
			return code && !body.message.includes(code) ? `${body.message} (${code})` : body.message;
		}
		if (typeof body.error === "string" && body.error !== "") return body.error;
	}
	return fallback;
}
