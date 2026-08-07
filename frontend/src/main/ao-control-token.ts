import { readStoredAccount, writeStoredAccount, type SafeStorageLike } from "./ao-account-store";
import { CONTROL_PLANE_TIMEOUT_MS, fetchWithDeadline, withDeadline } from "./request-deadline";

/**
 * The desktop install's control-plane-audience access token.
 *
 * `GET /api/v1/machines` and every other control plane API route accept exactly
 * one credential: an access token whose `aud` is the control plane's own origin
 * (controlplane/TOKEN_CONTRACT.md, "The two audiences"). A machine-audience
 * token, the browser session cookie, and the refresh token itself are each
 * rejected there. So the refresh token is presented here, at
 * `POST /api/v1/token`, and nowhere else: it is long-lived and high-value, and
 * sending it on a resource route would spread ninety days of account access
 * across logs and proxies where a fifteen minute token would have leaked
 * fifteen minutes.
 *
 * Refresh tokens rotate on use, so exchanging one revokes it in the same
 * transaction that issues its replacement. Two consequences shape this module:
 * the replacement is persisted before the access token is handed out, and
 * concurrent callers share one exchange rather than racing two, where the
 * loser would have presented an already-revoked token.
 *
 * Not in scope here: the machine-audience token, the Bearer header on a
 * machine's REST routes, the `/mux` and SSE cookie, and the silent background
 * refresh. Those are ao-machine-token.ts and machine-transport.ts, which take
 * the source built here rather than building a second one, because two sources
 * would race a rotation of the same refresh token.
 */

/** Refreshed this early before expiry, so a token never expires mid-request. */
const EXPIRY_SKEW_MS = 60_000;

/** Fallback when the token endpoint omits `expires_in`; the contract's default. */
const DEFAULT_TTL_SECONDS = 15 * 60;

/** Said when the rotated refresh token could not be stored. See exchange(). */
export const STORE_ROTATED_TOKEN_FAILURE =
	"This computer's AO sign-in could not be saved after being refreshed, so it is no longer valid. Sign in again.";

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export type ControlPlaneTokenDeps = {
	/** The ~/.ao state dir, where the encrypted refresh token lives. */
	stateDir: string;
	/** Origin of the control plane this install trusts, no trailing slash. */
	controlPlaneUrl: string;
	safeStorage: SafeStorageLike;
	fetchImpl?: typeof fetch;
	now?: () => number;
	/** Deadline for the token exchange. See request-deadline.ts. */
	timeoutMs?: number;
};

export type ControlPlaneTokenSource = {
	/** A usable access token, or null when this install is not signed in. */
	get: () => Promise<string | null>;
	/** Drop the cached token. Called on sign-out: nothing survives it. */
	clear: () => void;
};

type TokenResponse = {
	access_token?: unknown;
	expires_in?: unknown;
	refresh_token?: unknown;
};

function exchangeFailure(status: number, body: unknown): Error {
	const envelope = (body ?? {}) as { error?: unknown; error_description?: unknown };
	const code = typeof envelope.error === "string" ? envelope.error : "";
	const description = typeof envelope.error_description === "string" ? envelope.error_description : "";
	if (code === "invalid_grant") {
		return new Error("This computer's AO sign-in is no longer valid. Sign in again.");
	}
	return new Error(description || code || `The control plane returned ${status}.`);
}

export function createControlPlaneTokenSource(deps: ControlPlaneTokenDeps): ControlPlaneTokenSource {
	const fetchImpl = deps.fetchImpl ?? fetch;
	const now = deps.now ?? Date.now;
	const timeoutMs = deps.timeoutMs ?? CONTROL_PLANE_TIMEOUT_MS;

	let cached: { token: string; expiresAt: number } | null = null;
	let inFlight: Promise<string | null> | null = null;

	async function exchange(): Promise<string | null> {
		const stored = await readStoredAccount(deps.stateDir, deps.safeStorage);
		if (!stored) return null;

		const response = await fetchWithDeadline(
			fetchImpl,
			`${deps.controlPlaneUrl}/api/v1/token`,
			{
				method: "POST",
				headers: {
					"Content-Type": "application/x-www-form-urlencoded",
					Accept: "application/json",
				},
				body: new URLSearchParams({
					grant_type: "refresh_token",
					refresh_token: stored.refreshToken,
				}).toString(),
			},
			timeoutMs,
			"Refreshing this computer's sign-in",
		);

		// Bodies here carry a refresh token, so nothing about them is logged.
		let body: unknown = null;
		try {
			body = await response.json();
		} catch {
			body = null;
		}
		if (!response.ok) throw exchangeFailure(response.status, body);

		const { access_token: accessToken, expires_in: expiresIn, refresh_token: rotated } = (body ?? {}) as TokenResponse;
		if (typeof accessToken !== "string" || !accessToken) {
			throw new Error("The control plane did not return an access token.");
		}
		if (typeof rotated !== "string" || !rotated) {
			throw new Error("The control plane did not return a replacement refresh token.");
		}

		// Persisted before the access token is handed out: the presented refresh
		// token is already revoked, so losing the replacement here would lock this
		// install out until the user signs in again.
		try {
			await writeStoredAccount(deps.stateDir, deps.safeStorage, { account: stored.account, refreshToken: rotated });
		} catch (err) {
			// The exchange succeeded, which revoked the token still on disk. Whatever
			// stopped the write, the sign-in is gone and only signing in again brings
			// it back, so say that rather than reporting a filesystem error that reads
			// as something a retry would fix.
			throw new Error(`${STORE_ROTATED_TOKEN_FAILURE} (${errorMessage(err)})`);
		}

		const ttlSeconds = typeof expiresIn === "number" && expiresIn > 0 ? expiresIn : DEFAULT_TTL_SECONDS;
		cached = { token: accessToken, expiresAt: now() + ttlSeconds * 1000 };
		return accessToken;
	}

	return {
		async get(): Promise<string | null> {
			if (cached && cached.expiresAt - EXPIRY_SKEW_MS > now()) return cached.token;
			// One exchange at a time. A second concurrent exchange would present a
			// refresh token the first one had already rotated away.
			if (inFlight) return inFlight;
			// Deadlined around the WHOLE exchange, not just its fetch. `inFlight` is
			// handed to every later caller, and it is cleared in the `.finally`
			// below, which only runs when the promise settles. So a single exchange
			// that never settles would poison this source for the life of the
			// process: the machine list would spin forever with no request in
			// flight and no error, recoverable only by restarting the app. The race
			// guarantees settlement even if something other than the fetch hangs.
			const attempt = withDeadline(Promise.resolve().then(exchange), timeoutMs, "Refreshing this computer's sign-in")
				.finally(() => {
					inFlight = null;
				});
			inFlight = attempt;
			return attempt;
		},

		clear(): void {
			cached = null;
		},
	};
}
