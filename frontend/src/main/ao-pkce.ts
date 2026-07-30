import { createHash, randomBytes } from "node:crypto";

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

export type CallbackResult = { code: string } | { error: string };

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
		return { error: "The sign-in callback URL was malformed." };
	}

	const state = params.get("state") ?? "";
	if (!expectedState || state !== expectedState) {
		return { error: "The sign-in callback did not match this request and was rejected." };
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
