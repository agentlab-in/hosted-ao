import { formatLastSeen, type AoMachine } from "./ao-machines";
import type { DaemonStatus } from "./daemon-status";

export const REMOTE_PAIRING_COOKIE_NAME = "ao_hosted_pair";

/**
 * The VM gateway's JWT cookie, whose value is the current machine-audience
 * access token.
 *
 * It exists for the two routes a browser cannot put a header on: the `/mux`
 * WebSocket handshake, which has no header API, and the SSE stream at
 * `GET /api/v1/events`, which the renderer opens with `EventSource`, which has
 * none either. Every other route on the gateway takes `Authorization: Bearer`
 * and refuses this cookie, deliberately: it is ambient, so accepting it on a
 * state-changing method would leave the gateway's CORS origin check as the only
 * CSRF defence.
 *
 * Must stay equal to `gatewayCookieName` in
 * `backend/internal/vmgateway/proxy.go`. See `controlplane/TOKEN_CONTRACT.md`.
 */
export const GATEWAY_COOKIE_NAME = "ao_gw_token";

export type RemoteDaemonConfig = { baseUrl: string; token: string };

export function readRemoteDaemonConfig(env: Record<string, string | undefined>): RemoteDaemonConfig | null {
	const rawURL = env.AO_REMOTE_URL?.trim() ?? "";
	const token = env.AO_REMOTE_TOKEN?.trim() ?? "";
	if (!rawURL && !token) return null;
	if (!rawURL || !token) throw new Error("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	if (!/^[A-Za-z0-9_-]+$/.test(token)) throw new Error("AO_REMOTE_TOKEN must be URL-safe base64");
	const url = new URL(rawURL);
	if (url.protocol !== "https:" || url.pathname !== "/" || rawURL.includes("?") || rawURL.includes("#") || url.username || url.password) {
		throw new Error("AO_REMOTE_URL must be an HTTPS origin without a path, query, fragment, or credentials");
	}
	return { baseUrl: url.origin, token };
}

export function isRemoteDaemonBaseUrl(baseUrl: string): boolean {
	return baseUrl.startsWith("https://");
}

/** Reaching a machine, before it has answered or before it is authenticated. */
export function machineConnectingStatus(machine: AoMachine): DaemonStatus {
	return { state: "starting", message: `Connecting to ${machine.name}…` };
}

/**
 * The app's daemon status while a registered machine is the active one.
 *
 * An unreachable machine is an error status, not the absence of one, and that
 * is the whole point: createRemoteDaemonLifecycle only falls through to the
 * local daemon when there is no status here at all. A remote machine being
 * down therefore cannot spawn a local daemon, because the code path that
 * spawns one is never reached rather than being skipped by a condition.
 *
 * The `ready` case carries the machine's base URL, which is what re-points the
 * renderer's REST calls, its SSE stream, and the terminal mux at that gateway.
 * Only `main/machine-transport.ts` may publish it, and only once a
 * machine-audience token has been obtained and the `ao_gw_token` cookie is
 * installed for that origin. Publishing it earlier would point every one of
 * those at a gateway that answers 401 under a UI saying "Connected".
 *
 * Shared rather than main-process-only so browser preview shows the same
 * states, from the same copy, as the packaged app.
 */
export function machineDaemonStatus(machine: AoMachine): DaemonStatus {
	if (machine.reachability === "offline") {
		const seen = formatLastSeen(machine.lastSeen);
		return {
			state: "error",
			code: "daemon_unreachable",
			message: seen
				? `${machine.name} is not reachable. Last seen ${seen}.`
				: `${machine.name} is not reachable, and has never connected to AO.`,
		};
	}
	if (machine.reachability === "unknown") return machineConnectingStatus(machine);
	return { state: "ready", baseUrl: machine.baseUrl, message: `Connected to ${machine.name}` };
}

/**
 * A reachable machine the app has no usable credential for.
 *
 * Deliberately not a `ready` status: it carries no base URL, so nothing is
 * pointed at a gateway that would refuse it. `reason` is the failure as
 * reported, never a token or a cookie value.
 */
export function machineAuthFailedStatus(machine: AoMachine, reason: string): DaemonStatus {
	return {
		state: "error",
		code: "machine_auth_failed",
		message: `Could not sign in to ${machine.name}. ${reason}`,
	};
}
