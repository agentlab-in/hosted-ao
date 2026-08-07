import type { ControlPlaneTokenSource } from "./ao-control-token";
import { CONTROL_PLANE_TIMEOUT_MS, fetchWithDeadline, withDeadline } from "./request-deadline";

/**
 * The machine-audience access token for one registered machine.
 *
 * Every route on a machine's gateway wants an access token whose `aud` is that
 * machine's id (controlplane/TOKEN_CONTRACT.md, "The two audiences"). The
 * control-plane-audience token this install already holds is not one: the
 * gateway pins `aud` to its own machine id and rejects it, correctly. So the
 * machine token comes from `POST /api/v1/machines/{id}/token`, authenticated
 * with the control-plane token, which is the same credential
 * `GET /api/v1/machines` takes.
 *
 * That call rotates nothing and consumes nothing, so it is freely repeatable and
 * none of ao-control-token.ts's persist-before-use machinery applies here. The
 * refresh token is not presented on this path at all: it goes to
 * `POST /api/v1/token` and nowhere else.
 */

/** Refreshed this early before expiry, so a token never expires mid-request. */
const EXPIRY_SKEW_MS = 60_000;

/**
 * Fallback when the endpoint omits `expires_in`. The contract's default, but a
 * deployment may configure 10 to 30 minutes, so a returned value always wins.
 */
const DEFAULT_TTL_SECONDS = 15 * 60;

/** Said when the control plane will not issue a token for the active machine. */
export const MACHINE_UNAVAILABLE_MESSAGE = "This account no longer has that machine.";

/** Said when the control plane refuses this install's sign-in outright. */
export const SIGN_IN_REJECTED_MESSAGE = "The control plane rejected this computer's sign-in. Sign in again.";

/**
 * The 404 from the token endpoint, which is deliberately the same answer for a
 * machine that was revoked, one owned by another account, and one that never
 * existed, so a signed-in account is not an oracle for which machine ids exist.
 * Nothing here may infer which of the three it was: the machine list is where
 * that gets resolved.
 */
export class MachineUnavailableError extends Error {
	constructor(message = MACHINE_UNAVAILABLE_MESSAGE) {
		super(message);
		this.name = "MachineUnavailableError";
	}
}

export type MachineAccessToken = {
	token: string;
	/** Epoch ms, from the endpoint's own `expires_in`. Drives the silent refresh. */
	expiresAt: number;
};

export type MachineTokenSourceDeps = {
	/** Origin of the control plane this install trusts, no trailing slash. */
	controlPlaneUrl: string;
	/** `machines.id`, which is also the `aud` of the token this mints. */
	machineId: string;
	/** The one control-plane token source in this process. See ao-machines.ts. */
	controlToken: ControlPlaneTokenSource;
	fetchImpl?: typeof fetch;
	now?: () => number;
	/** Deadline for the mint call. See request-deadline.ts. */
	timeoutMs?: number;
};

export type MachineTokenSource = {
	/** A usable token, minting only when the cached one is spent. Null when signed out. */
	get: () => Promise<MachineAccessToken | null>;
	/**
	 * Mint unconditionally and replace the cache.
	 *
	 * The silent refresh runs on a longer lead than the skew above, so it must not
	 * go through get(): the cached token is still usable at that point, and get()
	 * would hand it straight back, leaving the refresh with nothing new to install
	 * and rescheduling itself into a spin. Asking for a fresh token explicitly is
	 * what the timer actually wants.
	 */
	mint: () => Promise<MachineAccessToken | null>;
	/** Drop the cached token. Called when the machine stops being active. */
	clear: () => void;
};

type MachineTokenResponse = {
	access_token?: unknown;
	expires_in?: unknown;
};

export function createMachineTokenSource(deps: MachineTokenSourceDeps): MachineTokenSource {
	const fetchImpl = deps.fetchImpl ?? fetch;
	const now = deps.now ?? Date.now;
	const timeoutMs = deps.timeoutMs ?? CONTROL_PLANE_TIMEOUT_MS;

	let cached: MachineAccessToken | null = null;
	let inFlight: Promise<MachineAccessToken | null> | null = null;

	async function mint(): Promise<MachineAccessToken | null> {
		const controlToken = await deps.controlToken.get();
		// Signed out. There is no credential for a registered machine without an
		// account, and the caller turns this into a status rather than a retry.
		if (!controlToken) return null;

		const response = await fetchWithDeadline(
			fetchImpl,
			`${deps.controlPlaneUrl}/api/v1/machines/${encodeURIComponent(deps.machineId)}/token`,
			{
				method: "POST",
				headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json" },
			},
			timeoutMs,
			"Minting a machine token",
		);
		if (response.status === 404) throw new MachineUnavailableError();
		if (response.status === 401) throw new Error(SIGN_IN_REJECTED_MESSAGE);

		// This body carries an access token, so nothing about it is logged.
		let body: unknown = null;
		try {
			body = await response.json();
		} catch {
			body = null;
		}
		if (!response.ok) {
			throw new Error(`The control plane returned ${response.status} issuing a machine access token.`);
		}

		const { access_token: accessToken, expires_in: expiresIn } = (body ?? {}) as MachineTokenResponse;
		if (typeof accessToken !== "string" || !accessToken) {
			throw new Error("The control plane did not return a machine access token.");
		}
		const ttlSeconds = typeof expiresIn === "number" && expiresIn > 0 ? expiresIn : DEFAULT_TTL_SECONDS;
		cached = { token: accessToken, expiresAt: now() + ttlSeconds * 1000 };
		return cached;
	}

	// One mint at a time. Not for safety, since nothing rotates on this endpoint,
	// but so a burst of REST calls arriving at expiry shares one exchange instead
	// of firing one request per call.
	function force(): Promise<MachineAccessToken | null> {
		if (inFlight) return inFlight;
		// Deadlined around the whole mint, for the same reason as the control-plane
		// source: `inFlight` is shared with every later caller and only cleared when
		// this settles, so one mint that never settles would strand every machine
		// token for the life of the process. See request-deadline.ts.
		const attempt = withDeadline(Promise.resolve().then(mint), timeoutMs, "Minting a machine token").finally(() => {
			inFlight = null;
		});
		inFlight = attempt;
		return attempt;
	}

	return {
		async get(): Promise<MachineAccessToken | null> {
			if (cached && cached.expiresAt - EXPIRY_SKEW_MS > now()) return cached;
			return force();
		},

		mint: force,

		clear(): void {
			cached = null;
		},
	};
}
