import type { AoMachine } from "../shared/ao-machines";
import type { DaemonStatus } from "../shared/daemon-status";
import {
	GATEWAY_COOKIE_NAME,
	machineAuthFailedStatus,
	machineConnectingStatus,
	machineDaemonStatus,
} from "../shared/remote-daemon";
import type { ControlPlaneTokenSource } from "./ao-control-token";
import {
	createMachineTokenSource,
	MachineUnavailableError,
	type MachineTokenSource,
	type MachineTokenSourceDeps,
} from "./ao-machine-token";

/**
 * The desktop's authenticated transport to the active machine's gateway.
 *
 * Two credentials, because the gateway accepts two and the browser can only
 * offer one on some routes:
 *
 * - `Authorization: Bearer` on REST, which the renderer attaches per request by
 *   asking the main process for the current token over IPC. This is the only
 *   credential the gateway takes on a state-changing route.
 * - The `ao_gw_token` cookie for `/mux` and for `GET /api/v1/events`. Neither
 *   the WebSocket handshake nor `EventSource` has a header API at all, so the
 *   same JWT travels in a cookie that this module installs, and which must be
 *   `SameSite=None` because `app://renderer` reaching `https://vm.example.com`
 *   is a cross-site context.
 *
 * The `ready` status, which is what re-points the renderer at the gateway, is
 * published only after both are in place. Ordering is the point: a base URL
 * handed out before the cookie exists would open the SSE stream and the terminal
 * mux against a gateway that answers 401.
 *
 * Tokens last fifteen minutes by default, so this also owns the silent refresh.
 * "Silent" means no status is emitted on a successful refresh: the gateway reads
 * the cookie at the handshake and at the SSE request and never again, so
 * replacing its value leaves an already-open stream running, while emitting a
 * status would re-run the renderer's base-URL plumbing and visibly reconnect it.
 */

/**
 * Refresh this long before expiry. Taken off the expiry the endpoint reported,
 * never off a fixed TTL: the default is fifteen minutes but a deployment may
 * configure anywhere from ten to thirty (controlplane/TOKEN_CONTRACT.md).
 */
const REFRESH_LEAD_MS = 120_000;

/** Floor on the refresh delay, so a short or clock-skewed TTL cannot spin. */
const MIN_REFRESH_DELAY_MS = 5_000;

/** How often to retry a failed refresh while the installed token is still good. */
const REFRESH_RETRY_MS = 30_000;

/** Said when this install has no account, so no machine token can exist. */
export const SIGNED_OUT_REASON = "This computer is signed out of AO.";

/** The subset of Electron's cookie store this module needs, so tests can fake it. */
export type GatewayCookieStore = {
	set: (details: Electron.CookiesSetDetails) => Promise<void>;
	remove: (url: string, name: string) => Promise<void>;
};

export type MachineTransportDeps = {
	cookies: GatewayCookieStore;
	/** Origin of the control plane this install trusts, no trailing slash. */
	controlPlaneUrl: string;
	/** The one control-plane token source in this process. See ao-machines.ts. */
	controlToken: ControlPlaneTokenSource;
	/**
	 * The app's daemon status for the active machine, or null when this computer
	 * is the active machine. Called only when the status actually changes; a
	 * successful silent refresh deliberately does not call it.
	 */
	onStatus: (status: DaemonStatus | null) => void;
	/**
	 * The control plane will not issue a token for the active machine. It answers
	 * revoked, someone else's, and never-existed identically, so the reason is not
	 * knowable here; refreshing the machine list is what resolves it.
	 */
	onMachineGone: () => void;
	fetchImpl?: typeof fetch;
	now?: () => number;
	/** Test seam, so a transport can be driven without a real token endpoint. */
	createTokenSource?: (deps: MachineTokenSourceDeps) => MachineTokenSource;
};

export type MachineTransport = {
	/** Point the transport at a machine, or at null for this computer. */
	setMachine: (machine: AoMachine | null) => void;
	/** The gateway bearer for a REST call, or null when there is none. */
	token: () => Promise<string | null>;
};

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function createMachineTransport(deps: MachineTransportDeps): MachineTransport {
	const now = deps.now ?? Date.now;
	const createTokenSource = deps.createTokenSource ?? createMachineTokenSource;

	let target: AoMachine | null = null;
	let tokens: MachineTokenSource | null = null;
	let refreshTimer: ReturnType<typeof setTimeout> | undefined;
	/** Expiry of the token currently in the cookie, or 0 when none is installed. */
	let installedExpiresAt = 0;
	/**
	 * Bumped by every setMachine. An acquisition that was already in flight when
	 * the user switched machines would otherwise land late and install machine A's
	 * cookie, and publish machine A's base URL, while machine B is active.
	 */
	let generation = 0;

	function stopRefresh(): void {
		if (refreshTimer) clearTimeout(refreshTimer);
		refreshTimer = undefined;
	}

	async function installCookie(machine: AoMachine, token: string): Promise<void> {
		await deps.cookies.set({
			url: machine.baseUrl,
			name: GATEWAY_COOKIE_NAME,
			value: token,
			path: "/",
			// SameSite=None is only honoured together with Secure, which is why the
			// pair is set together. Without None the browser does not attach the
			// cookie to the /mux handshake or the EventSource request at all, and
			// both fail 401 with nothing to see on the client.
			secure: true,
			httpOnly: true,
			sameSite: "no_restriction",
			// No `domain`: host-only, so the credential is scoped to this gateway
			// and never offered to a sibling host under the same registrable domain.
		});
	}

	async function dropCookie(machine: AoMachine): Promise<void> {
		try {
			await deps.cookies.remove(machine.baseUrl, GATEWAY_COOKIE_NAME);
		} catch {
			// Best effort. Switching away from a machine, or signing out, must not
			// leave an ambient credential behind, but a cookie jar that refuses the
			// removal is not a reason to fail the switch. The token in it expires on
			// its own within the fifteen minute window either way.
		}
	}

	function scheduleRefresh(startedAt: number, expiresAt: number, delayMs?: number): void {
		stopRefresh();
		const delay = delayMs ?? Math.max(expiresAt - REFRESH_LEAD_MS - now(), MIN_REFRESH_DELAY_MS);
		refreshTimer = setTimeout(() => {
			refreshTimer = undefined;
			void acquire(startedAt, "refresh");
		}, delay);
	}

	/**
	 * Get a token, install it in the cookie, and keep the refresh running.
	 *
	 * The reason is the whole difference between the two callers. A connect asks
	 * for a usable token, which may be one already cached from an earlier visit to
	 * this machine, and publishes the ready status that re-points the renderer. A
	 * refresh asks for a genuinely fresh one, because a cached token is exactly
	 * what it is there to replace, and publishes nothing at all.
	 */
	async function acquire(startedAt: number, reason: "connect" | "refresh"): Promise<void> {
		const machine = target;
		const source = tokens;
		if (!machine || !source || generation !== startedAt) return;
		const announce = reason === "connect";
		try {
			const minted = reason === "connect" ? await source.get() : await source.mint();
			if (generation !== startedAt) return;
			if (!minted) {
				deps.onStatus(machineAuthFailedStatus(machine, SIGNED_OUT_REASON));
				return;
			}
			await installCookie(machine, minted.token);
			if (generation !== startedAt) return;
			installedExpiresAt = minted.expiresAt;
			// The cookie is in place before the base URL is, so the renderer cannot
			// open the SSE stream or the terminal mux without a credential.
			if (announce) deps.onStatus(machineDaemonStatus(machine));
			scheduleRefresh(startedAt, minted.expiresAt);
		} catch (err) {
			if (generation !== startedAt) return;
			if (err instanceof MachineUnavailableError) {
				deps.onStatus(machineAuthFailedStatus(machine, err.message));
				deps.onMachineGone();
				return;
			}
			// The control plane going down while a user is already connected does not
			// disconnect them (the spec's failure behaviour): the installed token is
			// good for another couple of minutes and the gateway serves from its stale
			// JWKS cache. So retry quietly, and report only once the credential this
			// would have replaced has actually lapsed.
			if (installedExpiresAt > now()) {
				scheduleRefresh(startedAt, installedExpiresAt, REFRESH_RETRY_MS);
				return;
			}
			deps.onStatus(machineAuthFailedStatus(machine, errorMessage(err)));
		}
	}

	return {
		setMachine(machine: AoMachine | null): void {
			generation += 1;
			const startedAt = generation;
			stopRefresh();
			const previous = target;
			target = machine;
			installedExpiresAt = 0;

			if (previous && previous.baseUrl !== machine?.baseUrl) void dropCookie(previous);

			if (!machine) {
				tokens = null;
				deps.onStatus(null);
				return;
			}
			if (!tokens || previous?.id !== machine.id) {
				tokens = createTokenSource({
					controlPlaneUrl: deps.controlPlaneUrl,
					machineId: machine.id,
					controlToken: deps.controlToken,
					fetchImpl: deps.fetchImpl,
					now,
				});
			}
			// Nothing to authenticate to yet, and the status already says why. A
			// machine that is down must not be given a base URL, and must not cost a
			// token request either.
			if (machine.reachability !== "online") {
				deps.onStatus(machineDaemonStatus(machine));
				return;
			}
			deps.onStatus(machineConnectingStatus(machine));
			void acquire(startedAt, "connect");
		},

		async token(): Promise<string | null> {
			const source = tokens;
			if (!source) return null;
			try {
				return (await source.get())?.token ?? null;
			} catch {
				// The REST call that asked for this gets a 401 from the gateway and the
				// renderer already renders the machine's status; rejecting here would
				// surface as an unhandled error on the IPC channel instead.
				return null;
			}
		},
	};
}
