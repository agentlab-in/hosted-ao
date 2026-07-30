// @vitest-environment node
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { readStoredAccount, writeStoredAccount, type SafeStorageLike } from "./ao-account-store";
import { createControlPlaneTokenSource } from "./ao-control-token";

// Same stand-in as ao-account-store.test.ts: reversible, and different from the
// plaintext, which is all these tests need from it.
const safeStorage: SafeStorageLike = {
	isEncryptionAvailable: () => true,
	encryptString: (plain) => Buffer.from(Array.from(Buffer.from(plain, "utf8"), (b) => b ^ 0x5a)),
	decryptString: (cipher) => Buffer.from(Array.from(cipher, (b) => b ^ 0x5a)).toString("utf8"),
};

const account = { id: "acct_1", email: "dev@example.com" };
const CONTROL_PLANE = "https://ao.agentlab.in";

let stateDir = "";

beforeEach(async () => {
	stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-control-token-"));
});
afterEach(async () => {
	await rm(stateDir, { recursive: true, force: true });
});

function tokenResponse(body: Record<string, unknown>, status = 200) {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function source(fetchImpl: typeof fetch, now = () => 0) {
	return createControlPlaneTokenSource({ stateDir, controlPlaneUrl: CONTROL_PLANE, safeStorage, fetchImpl, now });
}

test("exchanges the refresh token at the token endpoint, and nowhere else", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	const fetchImpl = vi.fn(async () => tokenResponse({ access_token: "at_1", expires_in: 900, refresh_token: "rt_2" }));

	await expect(source(fetchImpl as unknown as typeof fetch).get()).resolves.toBe("at_1");

	expect(fetchImpl).toHaveBeenCalledTimes(1);
	const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
	expect(url).toBe("https://ao.agentlab.in/api/v1/token");
	expect(init.method).toBe("POST");
	// The refresh token is a body parameter of the token endpoint. It is never a
	// Bearer credential and never in the URL (controlplane/TOKEN_CONTRACT.md).
	expect(new URLSearchParams(init.body as string).get("refresh_token")).toBe("rt_1");
	expect(new URLSearchParams(init.body as string).get("grant_type")).toBe("refresh_token");
	expect(JSON.stringify(init.headers)).not.toContain("rt_1");
	expect(url).not.toContain("rt_1");
});

test("persists the rotated refresh token, because the presented one is already revoked", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	const fetchImpl = vi.fn(async () => tokenResponse({ access_token: "at_1", expires_in: 900, refresh_token: "rt_2" }));

	await source(fetchImpl as unknown as typeof fetch).get();

	const stored = await readStoredAccount(stateDir, safeStorage);
	expect(stored?.refreshToken).toBe("rt_2");
	expect(stored?.account).toEqual(account);
});

test("caches the access token until it is close to expiry", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	let now = 0;
	let issued = 0;
	const fetchImpl = vi.fn(async () => {
		issued += 1;
		return tokenResponse({ access_token: `at_${issued}`, expires_in: 900, refresh_token: "rt_next" });
	});
	const tokens = source(fetchImpl as unknown as typeof fetch, () => now);

	await expect(tokens.get()).resolves.toBe("at_1");
	now = 800_000;
	await expect(tokens.get()).resolves.toBe("at_1");
	expect(fetchImpl).toHaveBeenCalledTimes(1);

	// Inside the 60s skew window, so a fresh exchange rather than a token that
	// would expire mid-request.
	now = 860_000;
	await expect(tokens.get()).resolves.toBe("at_2");
	expect(fetchImpl).toHaveBeenCalledTimes(2);
});

test("concurrent callers share one exchange, so neither replays a rotated token", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	const fetchImpl = vi.fn(async () => tokenResponse({ access_token: "at_1", expires_in: 900, refresh_token: "rt_2" }));
	const tokens = source(fetchImpl as unknown as typeof fetch);

	await expect(Promise.all([tokens.get(), tokens.get(), tokens.get()])).resolves.toEqual(["at_1", "at_1", "at_1"]);
	expect(fetchImpl).toHaveBeenCalledTimes(1);
});

test("signed out returns no token and never calls the control plane", async () => {
	const fetchImpl = vi.fn();
	await expect(source(fetchImpl as unknown as typeof fetch).get()).resolves.toBeNull();
	expect(fetchImpl).not.toHaveBeenCalled();
});

test("a revoked or replayed refresh token says to sign in again", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	const fetchImpl = vi.fn(async () =>
		tokenResponse({ error: "invalid_grant", error_description: "the refresh token is not valid" }, 400),
	);

	await expect(source(fetchImpl as unknown as typeof fetch).get()).rejects.toThrow(/Sign in again/);
});

test("a response missing either token is a failure, not a half-applied rotation", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });

	const noAccess = vi.fn(async () => tokenResponse({ refresh_token: "rt_2" }));
	await expect(source(noAccess as unknown as typeof fetch).get()).rejects.toThrow(/did not return an access token/);

	const noRotation = vi.fn(async () => tokenResponse({ access_token: "at_1" }));
	await expect(source(noRotation as unknown as typeof fetch).get()).rejects.toThrow(/replacement refresh token/);
	// The refresh token on disk is untouched, so the next attempt can retry it.
	await expect(readStoredAccount(stateDir, safeStorage)).resolves.toMatchObject({ refreshToken: "rt_1" });
});

test("clear drops the cached token, so sign-out leaves nothing behind", async () => {
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_1" });
	const fetchImpl = vi.fn(async () => tokenResponse({ access_token: "at_1", expires_in: 900, refresh_token: "rt_2" }));
	const tokens = source(fetchImpl as unknown as typeof fetch);

	await tokens.get();
	tokens.clear();
	await tokens.get();
	expect(fetchImpl).toHaveBeenCalledTimes(2);
});
