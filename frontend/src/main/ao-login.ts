import type { AoAccount } from "../shared/ao-account";
import {
	buildAuthorizeUrl,
	codeChallenge,
	createCodeVerifier,
	createState,
	DESKTOP_CLIENT_ID,
	describeOauthError,
	TOKEN_PATH,
} from "./ao-pkce";
import { startLoopbackCallback, type LoopbackCallback } from "./loopback-callback";
import type { StoredAoAccount } from "./ao-account-store";
import { CONTROL_PLANE_TIMEOUT_MS, fetchWithDeadline } from "./request-deadline";

/**
 * The desktop sign-in flow: system browser, PKCE, loopback redirect (RFC 8252).
 *
 * Google blocks OAuth in embedded webviews, so the browser here is always the
 * user's real browser via shell.openExternal. There is no in-app BrowserWindow
 * login and no path that skips this exchange.
 *
 * Contract with the control plane: docs/desktop-login-contract.md.
 */

export type DesktopLoginDeps = {
	controlPlaneUrl: string;
	openExternal: (url: string) => Promise<void>;
	/** Injected for tests; defaults to the real loopback listener. */
	startCallback?: (expectedState: string) => Promise<LoopbackCallback>;
	fetchImpl?: typeof fetch;
	/** Deadline for the token exchange. See request-deadline.ts. Test seam. */
	timeoutMs?: number;
};

type TokenResponse = {
	refresh_token?: unknown;
	account?: { id?: unknown; email?: unknown };
	error?: unknown;
	error_description?: unknown;
};

export async function runDesktopLogin(deps: DesktopLoginDeps): Promise<StoredAoAccount> {
	const verifier = createCodeVerifier();
	const state = createState();
	const startCallback = deps.startCallback ?? startLoopbackCallback;
	const callback = await startCallback(state);

	try {
		await deps.openExternal(
			buildAuthorizeUrl({
				controlPlaneUrl: deps.controlPlaneUrl,
				redirectUri: callback.redirectUri,
				codeChallenge: codeChallenge(verifier),
				state,
			}),
		);
		const code = await callback.code;
		return await exchangeCode({
			controlPlaneUrl: deps.controlPlaneUrl,
			code,
			codeVerifier: verifier,
			redirectUri: callback.redirectUri,
			fetchImpl: deps.fetchImpl ?? fetch,
			timeoutMs: deps.timeoutMs ?? CONTROL_PLANE_TIMEOUT_MS,
		});
	} finally {
		// The listener closes itself on a delivered callback; this covers the abort
		// paths (openExternal failed, exchange threw) so no port is left bound.
		callback.close();
	}
}

type ExchangeInput = {
	controlPlaneUrl: string;
	code: string;
	codeVerifier: string;
	redirectUri: string;
	fetchImpl: typeof fetch;
	timeoutMs: number;
};

async function exchangeCode(input: ExchangeInput): Promise<StoredAoAccount> {
	const body = new URLSearchParams({
		grant_type: "authorization_code",
		client_id: DESKTOP_CLIENT_ID,
		code: input.code,
		code_verifier: input.codeVerifier,
		redirect_uri: input.redirectUri,
	});

	let response: Response;
	try {
		// Deadlined like every other control-plane call (request-deadline.ts): this
		// is the one that used to be a bare fetch with no AbortController. ao-account.ts
		// holds a single `inFlight` promise for sign-in, cleared only in a `finally`,
		// so a blackholed token POST would otherwise never settle and would poison
		// sign-in for the rest of the process's life, exactly like the hazard
		// ao-control-token.ts's `inFlight` already guards against.
		response = await fetchWithDeadline(
			input.fetchImpl,
			`${input.controlPlaneUrl}${TOKEN_PATH}`,
			{
				method: "POST",
				headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
				body: body.toString(),
			},
			input.timeoutMs,
			"Signing in",
		);
	} catch {
		// Spec: control plane unreachable at login is reported plainly, and local
		// mode still works without it.
		throw new Error(
			`Could not reach the AO control plane at ${input.controlPlaneUrl}. Check your connection, or keep working in local mode, which needs no account.`,
		);
	}

	let payload: TokenResponse = {};
	try {
		payload = (await response.json()) as TokenResponse;
	} catch {
		// Fall through to the status-based message below.
	}

	if (!response.ok) {
		if (typeof payload.error === "string") {
			throw new Error(
				describeOauthError(payload.error, typeof payload.error_description === "string" ? payload.error_description : null),
			);
		}
		throw new Error(`The AO control plane rejected the sign-in (HTTP ${response.status}).`);
	}

	const refreshToken = typeof payload.refresh_token === "string" ? payload.refresh_token : "";
	const account = readAccount(payload.account);
	if (!refreshToken || !account) {
		throw new Error("The AO control plane returned an unexpected sign-in response.");
	}
	return { account, refreshToken };
}

function readAccount(raw: TokenResponse["account"]): AoAccount | null {
	if (!raw || typeof raw !== "object") return null;
	const id = typeof raw.id === "string" ? raw.id : "";
	const email = typeof raw.email === "string" ? raw.email : "";
	if (!id) return null;
	return { id, email };
}
