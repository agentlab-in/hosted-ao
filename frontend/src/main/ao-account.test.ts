// @vitest-environment node
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createAoAccountController, type AoAccountControllerDeps } from "./ao-account";
import { AO_ACCOUNT_FILE_NAME, type SafeStorageLike } from "./ao-account-store";
import { codeChallenge } from "./ao-pkce";
import type { LoopbackCallback } from "./loopback-callback";

const account = { id: "acct_1", email: "dev@example.com" };

function fakeSafeStorage(available = true): SafeStorageLike {
	return {
		isEncryptionAvailable: () => available,
		encryptString: (plain) => Buffer.from(Array.from(Buffer.from(plain, "utf8"), (b) => b ^ 0x5a)),
		decryptString: (cipher) => Buffer.from(Array.from(cipher, (b) => b ^ 0x5a)).toString("utf8"),
	};
}

/** A loopback listener that reports a code without any browser or socket. */
function fakeCallback(result: Promise<string> | string): (state: string) => Promise<LoopbackCallback> {
	return async () => ({
		redirectUri: "http://127.0.0.1:51234/callback",
		code: typeof result === "string" ? Promise.resolve(result) : result,
		close: () => undefined,
	});
}

let stateDir = "";
let opened: string[] = [];

function deps(overrides: Partial<AoAccountControllerDeps> = {}): AoAccountControllerDeps {
	return {
		stateDir,
		env: {},
		safeStorage: fakeSafeStorage(),
		openExternal: async (url) => {
			opened.push(url);
		},
		startCallback: fakeCallback("code_abc"),
		fetchImpl: async () =>
			new Response(JSON.stringify({ refresh_token: "rt_secret", account }), {
				status: 200,
				headers: { "content-type": "application/json" },
			}),
		...overrides,
	};
}

beforeEach(async () => {
	stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-account-ctl-"));
	opened = [];
});
afterEach(async () => {
	await rm(stateDir, { recursive: true, force: true });
});

test("a fresh install is signed out, and says which control plane it trusts", async () => {
	const controller = createAoAccountController(deps());
	await expect(controller.getState()).resolves.toEqual({
		status: "signed-out",
		controlPlaneUrl: "https://ao.agentlab.in",
	});
});

test("sign-in opens the system browser, exchanges the code with PKCE, and stores the token", async () => {
	const fetchImpl = vi.fn(deps().fetchImpl as typeof fetch);
	const controller = createAoAccountController(deps({ env: { AO_CONTROL_URL: "http://127.0.0.1:8080" }, fetchImpl }));

	await expect(controller.signIn()).resolves.toEqual({
		status: "signed-in",
		controlPlaneUrl: "http://127.0.0.1:8080",
		account,
	});

	// Exactly one authorization URL, opened externally, against the dev control plane.
	expect(opened).toHaveLength(1);
	const authorize = new URL(opened[0]);
	expect(authorize.origin + authorize.pathname).toBe("http://127.0.0.1:8080/oauth/desktop/authorize");
	expect(authorize.searchParams.get("code_challenge_method")).toBe("S256");
	const state = authorize.searchParams.get("state") ?? "";
	expect(state).not.toBe("");

	const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
	expect(url).toBe("http://127.0.0.1:8080/oauth/desktop/token");
	const body = new URLSearchParams(String(init.body));
	expect(body.get("grant_type")).toBe("authorization_code");
	expect(body.get("code")).toBe("code_abc");
	expect(body.get("redirect_uri")).toBe("http://127.0.0.1:51234/callback");
	// The verifier sent to the token endpoint is the pre-image of the challenge the
	// browser was sent, which is the whole point of PKCE.
	expect(codeChallenge(body.get("code_verifier") ?? "")).toBe(authorize.searchParams.get("code_challenge"));

	// Persisted, encrypted, and readable again through a new controller.
	const raw = await readFile(path.join(stateDir, AO_ACCOUNT_FILE_NAME), "utf8");
	expect(raw).not.toContain("rt_secret");
	await expect(createAoAccountController(deps()).getState()).resolves.toMatchObject({ status: "signed-in", account });
});

test("sign-out discards the stored refresh token, not just the UI state", async () => {
	const controller = createAoAccountController(deps());
	await controller.signIn();
	await expect(controller.signOut()).resolves.toEqual({
		status: "signed-out",
		controlPlaneUrl: "https://ao.agentlab.in",
	});
	await expect(readFile(path.join(stateDir, AO_ACCOUNT_FILE_NAME), "utf8")).rejects.toThrow();
	// A new controller cannot resurrect it either: the credential is gone from disk.
	await expect(createAoAccountController(deps()).getState()).resolves.toMatchObject({ status: "signed-out" });
});

test("a rejected callback leaves the app signed out with the reason", async () => {
	const controller = createAoAccountController(
		deps({ startCallback: async () => ({
			redirectUri: "http://127.0.0.1:51234/callback",
			code: Promise.reject(new Error("The sign-in callback did not match this request and was rejected.")),
			close: () => undefined,
		}) }),
	);
	const state = await controller.signIn();
	expect(state.status).toBe("signed-out");
	expect(state.error).toMatch(/did not match/);
	await expect(readFile(path.join(stateDir, AO_ACCOUNT_FILE_NAME), "utf8")).rejects.toThrow();
});

test("an unreachable control plane is reported plainly and points at local mode", async () => {
	const controller = createAoAccountController(
		deps({
			fetchImpl: async () => {
				throw new Error("ECONNREFUSED");
			},
		}),
	);
	const state = await controller.signIn();
	expect(state.status).toBe("signed-out");
	expect(state.error).toMatch(/Could not reach the AO control plane/);
	expect(state.error).toMatch(/local mode/);
});

test("an OAuth error from the token endpoint surfaces its description", async () => {
	const controller = createAoAccountController(
		deps({
			fetchImpl: async () =>
				new Response(JSON.stringify({ error: "invalid_grant", error_description: "code already used" }), {
					status: 400,
					headers: { "content-type": "application/json" },
				}),
		}),
	);
	await expect(controller.signIn()).resolves.toMatchObject({
		status: "signed-out",
		error: "Sign-in failed: code already used",
	});
});

test("no OS credential store means sign-in fails loudly and no browser is opened", async () => {
	const controller = createAoAccountController(deps({ safeStorage: fakeSafeStorage(false) }));
	const state = await controller.signIn();
	expect(state.status).toBe("unavailable");
	expect(state.error).toMatch(/plaintext/);
	expect(opened).toEqual([]);
});

test("an unusable AO_CONTROL_URL blocks sign-in instead of falling back to production", async () => {
	const controller = createAoAccountController(deps({ env: { AO_CONTROL_URL: "http://ao.agentlab.in" } }));
	await expect(controller.getState()).resolves.toMatchObject({ status: "unavailable" });
	const state = await controller.signIn();
	expect(state.status).toBe("unavailable");
	expect(opened).toEqual([]);
});

test("a second sign-in click joins the attempt already in flight", async () => {
	let release: (code: string) => void = () => undefined;
	const pending = new Promise<string>((resolve) => {
		release = resolve;
	});
	const controller = createAoAccountController(deps({ startCallback: fakeCallback(pending) }));

	const first = controller.signIn();
	// Give the flow a turn to open the browser before the second click lands.
	await Promise.resolve();
	const second = controller.signIn();
	await expect(controller.getState()).resolves.toMatchObject({ status: "signing-in" });

	release("code_abc");
	await expect(first).resolves.toMatchObject({ status: "signed-in" });
	await expect(second).resolves.toMatchObject({ status: "signed-in" });
	// One browser tab, one state, one exchange.
	expect(opened).toHaveLength(1);
});

test("with no resolvable ~/.ao state dir, sign-in is unavailable rather than silent", async () => {
	const controller = createAoAccountController(deps({ stateDir: null }));
	await expect(controller.getState()).resolves.toMatchObject({ status: "unavailable" });
	await expect(controller.signIn()).resolves.toMatchObject({ status: "unavailable" });
	expect(opened).toEqual([]);
});
