import type { AoMachine } from "../shared/ao-machines";
import type { DaemonStatus } from "../shared/daemon-status";
import {
	GATEWAY_COOKIE_NAME,
	machineAuthFailedStatus,
	machineConnectingStatus,
	machineDaemonStatus,
} from "../shared/remote-daemon";
export type GatewayCookieStore = {
	set: (details: Electron.CookiesSetDetails) => Promise<void>;
	remove: (url: string, name: string) => Promise<void>;
};

/**
 * The desktop's transport to a paired machine's gateway
 * (docs/adr/0003-pair-mode-gateway.md, docs/plans/2026-08-16-pair-by-ip-headless-boxes.md task 9).
 *
 * Sibling to machine-transport.ts, not a variant of it. Pair mode's whole
 * point is no control plane, so unlike the hosted transport there is no token
 * to mint, no expiry, and no silent refresh here: the credential is the
 * passcode the user entered once, decrypted by paired-machines.ts and handed
 * in by the caller. It travels exactly the way the gateway's `requirePasscode`
 * expects (backend/internal/vmgateway/passcode.go), because `requirePasscode`
 * reads it with the same `extractToken` `requireToken` uses for hosted mode:
 * `Authorization: Bearer <passcode>` on REST, attached per request by the
 * renderer via `aoMachines:gatewayToken`, and the same `ao_gw_token` cookie
 * for the two routes a browser cannot put a header on -- the `/mux` terminal
 * WebSocket and the SSE event stream (see shared/remote-daemon.ts). No new
 * scheme, just the existing one carrying a passcode instead of a JWT.
 *
 * A rotated passcode on the box (docs/adr/0003) presents as a standing 401
 * against a still-installed cookie. Recovering from that is the re-pair flow
 * (task 8), not this transport's job.
 */

const NO_PASSCODE_REASON = "No stored passcode is available for this machine. Re-pair it.";

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

/** The subset of Electron's cookie store this module needs; same shape as machine-transport.ts's, so both can share `session.defaultSession.cookies` untyped-widened. */
export type PairedGatewayCookieStore = GatewayCookieStore;

export type PairedMachineTransportDeps = {
	cookies: PairedGatewayCookieStore;
	/**
	 * The app's daemon status while this transport's machine is the active one,
	 * or null once it no longer is. Same shape as machine-transport.ts's
	 * `onStatus`, so main.ts can point either transport's callback at the same
	 * sink and switch between them by which one is allowed to write.
	 */
	onStatus: (status: DaemonStatus | null) => void;
};

export type PairedMachineTransport = {
	/**
	 * Point the transport at a paired machine and its decrypted passcode, or at
	 * null to release it. The passcode is asked for by the caller (main.ts, via
	 * paired-machines.ts's `getPasscode`) rather than looked up in here, so this
	 * module never touches safeStorage or the encrypted store directly.
	 */
	setMachine: (machine: AoMachine | null, passcode: string | null) => void;
	/**
	 * The passcode for the current machine, or null when there is none.
	 * `forceRefresh` is accepted and ignored -- there is nothing to refresh --
	 * only so main.ts can route the `aoMachines:gatewayToken` IPC handler to
	 * whichever transport is active without branching on a different shape.
	 */
	token: (forceRefresh?: boolean) => string | null;
};

export function createPairedMachineTransport(deps: PairedMachineTransportDeps): PairedMachineTransport {
	let target: AoMachine | null = null;
	let currentPasscode: string | null = null;
	/**
	 * Bumped by every setMachine, so a cookie install still in flight from a
	 * superseded call cannot publish a status for a machine that is no longer
	 * the target (mirrors machine-transport.ts's generation guard).
	 */
	let generation = 0;

	async function installCookie(machine: AoMachine, passcode: string): Promise<void> {
		await deps.cookies.set({
			url: machine.baseUrl,
			name: GATEWAY_COOKIE_NAME,
			value: passcode,
			path: "/",
			// SameSite=None + Secure, same reasoning as machine-transport.ts: without
			// it the browser will not attach the cookie to the /mux handshake or the
			// EventSource request at all.
			secure: true,
			httpOnly: true,
			sameSite: "no_restriction",
		});
	}

	async function dropCookie(machine: AoMachine): Promise<void> {
		try {
			await deps.cookies.remove(machine.baseUrl, GATEWAY_COOKIE_NAME);
		} catch {
			// Best effort, mirrors machine-transport.ts's dropCookie: a cookie jar
			// that refuses removal is not a reason to fail the switch.
		}
	}

	return {
		setMachine(machine, passcode) {
			generation += 1;
			const startedAt = generation;
			const previous = target;
			target = machine;

			if (previous && previous.baseUrl !== machine?.baseUrl) void dropCookie(previous);

			if (!machine) {
				currentPasscode = null;
				deps.onStatus(null);
				return;
			}

			if (!passcode) {
				// Never say why in more detail than this: the caller's failure (a
				// decrypt error, an unavailable OS credential store) is reported by
				// paired-machines.ts's own error, not repeated here, and neither
				// message ever carries the passcode itself.
				currentPasscode = null;
				deps.onStatus(machineAuthFailedStatus(machine, NO_PASSCODE_REASON));
				return;
			}

			currentPasscode = passcode;

			// Nothing to authenticate to yet, and the status already says why. Mirrors
			// machine-transport.ts: an unreachable machine must not be given a base
			// URL, and must not cost a cookie install either.
			if (machine.reachability !== "online") {
				deps.onStatus(machineDaemonStatus(machine));
				return;
			}

			deps.onStatus(machineConnectingStatus(machine));
			// The cookie must be in place before the ready status is published, same
			// ordering rule as machine-transport.ts: a base URL handed out first would
			// open /mux and the SSE stream against a gateway with no credential
			// installed yet.
			void installCookie(machine, passcode)
				.then(() => {
					if (generation !== startedAt) return;
					deps.onStatus(machineDaemonStatus(machine));
				})
				.catch((err: unknown) => {
					if (generation !== startedAt) return;
					deps.onStatus(machineAuthFailedStatus(machine, errorMessage(err)));
				});
		},

		token() {
			return currentPasscode;
		},
	};
}
