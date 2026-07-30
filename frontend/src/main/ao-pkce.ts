import { createHash, randomBytes, timingSafeEqual } from "node:crypto";

/**
 * PKCE (RFC 7636) and authorization-request helpers for the desktop login flow.
 *
 * The desktop app is a public client: it ships no secret, so the code challenge
 * plus the `state` check are the whole defence against another local process
 * stealing the authorization code off the loopback redirect.
 *
 * Nothing in this module logs. The verifier and the code are secrets for the
 * length of one exchange and must never reach stdout, a log file, or telemetry.
 */

/** The client id the control plane knows the desktop app by. Public, not a secret. */
export const DESKTOP_CLIENT_ID = "ao-desktop";

/** Authorization endpoint on the control plane (see docs/desktop-login-contract.md). */
export const AUTHORIZE_PATH = "/oauth/desktop/authorize";
/** Token endpoint on the control plane. */
export const TOKEN_PATH = "/oauth/desktop/token";

const VERIFIER_BYTES = 32;
const STATE_BYTES = 32;

/** 43-char base64url verifier from 32 CSPRNG bytes, the RFC 7636 recommendation. */
export function createCodeVerifier(): string {
	return randomBytes(VERIFIER_BYTES).toString("base64url");
}

/** Opaque CSPRNG `state`, bound to this one authorization request. */
export function createState(): string {
	return randomBytes(STATE_BYTES).toString("base64url");
}

/** S256 challenge: base64url(SHA-256(verifier)). `plain` is never used. */
export function codeChallenge(verifier: string): string {
	return createHash("sha256").update(verifier).digest("base64url");
}

export type AuthorizeUrlInput = {
	controlPlaneUrl: string;
	redirectUri: string;
	codeChallenge: string;
	state: string;
};

export function buildAuthorizeUrl(input: AuthorizeUrlInput): string {
	const url = new URL(AUTHORIZE_PATH, `${input.controlPlaneUrl}/`);
	url.search = new URLSearchParams({
		response_type: "code",
		client_id: DESKTOP_CLIENT_ID,
		redirect_uri: input.redirectUri,
		code_challenge: input.codeChallenge,
		code_challenge_method: "S256",
		state: input.state,
	}).toString();
	return url.toString();
}

export type CallbackResult =
	| { code: string }
	// The authorization server said no, and it proved it was answering our own
	// request by carrying our `state`. This ends the sign-in.
	| { error: string }
	// Not our callback: wrong `state`, or a URL we could not read one out of. Says
	// nothing about the sign-in in flight, so the caller must not end it on this.
	| { mismatch: string };

/** Timing-safe `state` comparison. Unequal lengths are a mismatch, cheaply. */
function stateMatches(actual: string, expected: string): boolean {
	if (!expected) return false;
	// Byte lengths, not character counts: timingSafeEqual throws on a length
	// mismatch, and a multibyte callback value can match in characters and not
	// in bytes.
	const got = Buffer.from(actual);
	const want = Buffer.from(expected);
	return got.length === want.length && timingSafeEqual(got, want);
}

/**
 * Validate a loopback callback. `state` is checked before anything else is read,
 * so a callback that did not come from our own authorization request is rejected
 * rather than exchanged.
 */
export function parseCallback(rawUrl: string, expectedState: string): CallbackResult {
	let params: URLSearchParams;
	try {
		params = new URL(rawUrl, "http://127.0.0.1").searchParams;
	} catch {
		return { mismatch: "The sign-in callback URL was malformed." };
	}

	if (!stateMatches(params.get("state") ?? "", expectedState)) {
		return { mismatch: "The sign-in callback did not match this request and was rejected." };
	}

	const oauthError = params.get("error");
	if (oauthError) {
		return { error: describeOauthError(oauthError, params.get("error_description")) };
	}

	const code = params.get("code") ?? "";
	if (!code) return { error: "The sign-in callback carried no authorization code." };
	return { code };
}

export function describeOauthError(error: string, description?: string | null): string {
	if (error === "access_denied") return "Sign-in was declined.";
	const detail = description?.trim();
	return detail ? `Sign-in failed: ${detail}` : `Sign-in failed (${error}).`;
}
