// @vitest-environment node
import { createHash } from "node:crypto";
import { expect, test } from "vitest";
import {
	buildAuthorizeUrl,
	codeChallenge,
	createCodeVerifier,
	createState,
	DESKTOP_CLIENT_ID,
	parseCallback,
} from "./ao-pkce";

test("verifier and state are unguessable, distinct, and URL-safe", () => {
	const verifiers = new Set(Array.from({ length: 50 }, () => createCodeVerifier()));
	expect(verifiers.size).toBe(50);
	for (const verifier of verifiers) {
		// 32 CSPRNG bytes as base64url: 43 chars, inside RFC 7636's 43..128 range.
		expect(verifier).toMatch(/^[A-Za-z0-9_-]{43}$/);
	}
	expect(new Set(Array.from({ length: 50 }, () => createState())).size).toBe(50);
	expect(createState()).toMatch(/^[A-Za-z0-9_-]{43}$/);
});

test("challenge is base64url(SHA-256(verifier)), not the verifier itself", () => {
	const verifier = createCodeVerifier();
	const challenge = codeChallenge(verifier);
	expect(challenge).toBe(createHash("sha256").update(verifier).digest("base64url"));
	expect(challenge).not.toBe(verifier);
});

test("authorize URL carries S256 PKCE, the loopback redirect, and state", () => {
	const url = new URL(
		buildAuthorizeUrl({
			controlPlaneUrl: "http://127.0.0.1:8080",
			redirectUri: "http://127.0.0.1:51234/callback",
			codeChallenge: "chal",
			state: "st",
		}),
	);
	expect(url.origin + url.pathname).toBe("http://127.0.0.1:8080/oauth/desktop/authorize");
	expect(Object.fromEntries(url.searchParams)).toEqual({
		response_type: "code",
		client_id: DESKTOP_CLIENT_ID,
		redirect_uri: "http://127.0.0.1:51234/callback",
		code_challenge: "chal",
		code_challenge_method: "S256",
		state: "st",
	});
});

test("a matching callback yields the code", () => {
	expect(parseCallback("/callback?code=abc&state=st", "st")).toEqual({ code: "abc" });
});

test("a callback whose state does not match is a mismatch, not an error", () => {
	// `mismatch`, not `error`: the caller must keep waiting for the real callback
	// rather than ending the sign-in on a stray hit. Same length as the
	// expectation, and one character longer, both mismatch.
	expect(parseCallback("/callback?code=abc&state=xt", "st")).toEqual({
		mismatch: expect.stringContaining("did not match"),
	});
	expect(parseCallback("/callback?code=abc&state=stt", "st")).toEqual({
		mismatch: expect.stringContaining("did not match"),
	});
	// A missing state is a mismatch too, and an empty expectation never matches.
	expect(parseCallback("/callback?code=abc", "st")).toEqual({ mismatch: expect.stringContaining("did not match") });
	expect(parseCallback("/callback?code=abc&state=", "")).toEqual({ mismatch: expect.stringContaining("did not match") });
	// A multibyte state must compare as a mismatch, not throw on byte length.
	expect(parseCallback(`/callback?code=abc&state=${encodeURIComponent("é")}`, "st")).toEqual({
		mismatch: expect.stringContaining("did not match"),
	});
});

test("an OAuth error is reported, and access_denied reads as a decline", () => {
	expect(parseCallback("/callback?error=access_denied&state=st", "st")).toEqual({ error: "Sign-in was declined." });
	expect(parseCallback("/callback?error=server_error&error_description=boom&state=st", "st")).toEqual({
		error: "Sign-in failed: boom",
	});
});

test("a state-matching callback with no code is rejected", () => {
	expect(parseCallback("/callback?state=st", "st")).toEqual({ error: expect.stringContaining("no authorization code") });
});
