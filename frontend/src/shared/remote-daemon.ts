import { formatLastSeen, type AoMachine } from "./ao-machines";
import type { DaemonStatus } from "./daemon-status";

export const REMOTE_PAIRING_COOKIE_NAME = "ao_hosted_pair";

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

/**
 * The app's daemon status while a registered machine is the active one.
 *
 * An unreachable machine is an error status, not the absence of one, and that
 * is the whole point: createRemoteDaemonLifecycle only falls through to the
 * local daemon when there is no status here at all. A remote machine being
 * down therefore cannot spawn a local daemon, because the code path that
 * spawns one is never reached rather than being skipped by a condition.
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
	if (machine.reachability === "unknown") {
		return { state: "starting", message: `Connecting to ${machine.name}…` };
	}
	return machineTransportMissingStatus(machine);
}

/**
 * TASK 13 PLACEHOLDER. Delete this function and return
 * `{ state: "ready", baseUrl: machine.baseUrl, message: "Connected to ..." }`
 * from machineDaemonStatus when the remote transport lands.
 *
 * A reachable machine is not a usable one yet: every route on it wants a
 * machine-audience access token, and the desktop mints none (see the scope note
 * at the top of main/ao-control-token.ts). Reporting ready would point the
 * renderer at the gateway and turn every REST call and both EventSources into
 * 401s under a UI that says "Connected". Naming the missing piece is the honest
 * answer until there is a credential to send.
 */
function machineTransportMissingStatus(machine: AoMachine): DaemonStatus {
	return {
		state: "error",
		code: "not_configured",
		message: `${machine.name} is up, but this build cannot sign in to it yet. Reaching a registered machine needs the remote transport, which is not in this build. Use this computer for now.`,
	};
}
