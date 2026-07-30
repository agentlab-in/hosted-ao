import type { AoAccountState } from "../shared/ao-account";
import { readControlPlaneUrl } from "../shared/control-plane";
import {
	clearStoredAccount,
	readStoredAccount,
	SAFE_STORAGE_UNAVAILABLE_MESSAGE,
	writeStoredAccount,
	type SafeStorageLike,
} from "./ao-account-store";
import { runDesktopLogin, type DesktopLoginDeps } from "./ao-login";

/**
 * Main-process owner of the desktop app's AO sign-in state.
 *
 * Signing in is only ever needed to reach a remote machine. Local mode does not
 * consult this at all, so nothing here gates app startup or the local daemon.
 */

export type AoAccountControllerDeps = {
	/** The ~/.ao state dir, or null when the home dir is unresolvable. */
	stateDir: string | null;
	env: Record<string, string | undefined>;
	safeStorage: SafeStorageLike;
	openExternal: (url: string) => Promise<void>;
	/** Test seams, forwarded to the login flow. */
	startCallback?: DesktopLoginDeps["startCallback"];
	fetchImpl?: typeof fetch;
};

export type AoAccountController = {
	getState: () => Promise<AoAccountState>;
	signIn: () => Promise<AoAccountState>;
	signOut: () => Promise<AoAccountState>;
};

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function createAoAccountController(deps: AoAccountControllerDeps): AoAccountController {
	// Resolved once: AO_CONTROL_URL selects which control plane to trust, and a bad
	// value must be visible in the UI rather than silently falling back to production.
	let configError: string | null = null;
	let controlPlaneUrl = "";
	try {
		controlPlaneUrl = readControlPlaneUrl(deps.env);
	} catch (err) {
		configError = errorMessage(err);
	}

	let inFlight: Promise<AoAccountState> | null = null;
	// Survives a failed attempt so the settings row can explain what went wrong.
	let lastError: string | null = null;

	const state = (status: AoAccountState["status"], extra?: Partial<AoAccountState>): AoAccountState => ({
		status,
		controlPlaneUrl,
		...extra,
	});

	async function currentState(): Promise<AoAccountState> {
		if (configError) return state("unavailable", { error: configError });
		if (!deps.stateDir) {
			return state("unavailable", { error: "Could not resolve the ~/.ao state directory, so sign-in cannot be stored." });
		}
		if (inFlight) return state("signing-in");
		try {
			const stored = await readStoredAccount(deps.stateDir, deps.safeStorage);
			if (!stored) return state("signed-out", lastError ? { error: lastError } : undefined);
			return state("signed-in", { account: stored.account });
		} catch (err) {
			// A stored token that cannot be decrypted, or no credential store at all.
			// Signed-out with the reason: the app never falls back to plaintext.
			return state(deps.safeStorage.isEncryptionAvailable() ? "signed-out" : "unavailable", {
				error: errorMessage(err),
			});
		}
	}

	async function performSignIn(stateDir: string): Promise<AoAccountState> {
		// Checked before opening a browser: on a system with no credential store there
		// is nowhere to put the refresh token, so failing here beats failing after the
		// user has already signed in with Google.
		if (!deps.safeStorage.isEncryptionAvailable()) {
			return state("unavailable", { error: SAFE_STORAGE_UNAVAILABLE_MESSAGE });
		}
		const stored = await runDesktopLogin({
			controlPlaneUrl,
			openExternal: deps.openExternal,
			startCallback: deps.startCallback,
			fetchImpl: deps.fetchImpl,
		});
		await writeStoredAccount(stateDir, deps.safeStorage, stored);
		return state("signed-in", { account: stored.account });
	}

	return {
		getState: currentState,

		async signIn(): Promise<AoAccountState> {
			if (configError) return state("unavailable", { error: configError });
			if (!deps.stateDir) return currentState();
			// One login at a time: a second click joins the running attempt instead of
			// opening a second browser tab with a second state.
			if (inFlight) return inFlight;
			const stateDir = deps.stateDir;
			// Deferred by one microtask so `inFlight` is assigned before the body can
			// clear it, whatever the flow does.
			const attempt = Promise.resolve().then(async () => {
				try {
					lastError = null;
					return await performSignIn(stateDir);
				} catch (err) {
					lastError = errorMessage(err);
					return state("signed-out", { error: lastError });
				} finally {
					inFlight = null;
				}
			});
			inFlight = attempt;
			return attempt;
		},

		async signOut(): Promise<AoAccountState> {
			lastError = null;
			// Discards the refresh token itself, not just the UI flag. The control plane
			// keeps its own revocation list; this end stops holding the credential.
			if (deps.stateDir) await clearStoredAccount(deps.stateDir);
			return currentState();
		},
	};
}
