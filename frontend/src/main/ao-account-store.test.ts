// @vitest-environment node
import { mkdtemp, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test } from "vitest";
import {
	AO_ACCOUNT_FILE_NAME,
	clearStoredAccount,
	readStoredAccount,
	SAFE_STORAGE_UNAVAILABLE_MESSAGE,
	writeStoredAccount,
	type SafeStorageLike,
} from "./ao-account-store";

// Stand-in for the OS credential store. XOR is not encryption; it only has to be
// reversible and to make the on-disk bytes different from the plaintext, which is
// what these tests assert about.
function fakeSafeStorage(available = true): SafeStorageLike {
	return {
		isEncryptionAvailable: () => available,
		encryptString: (plain) => Buffer.from(Array.from(Buffer.from(plain, "utf8"), (b) => b ^ 0x5a)),
		decryptString: (cipher) => Buffer.from(Array.from(cipher, (b) => b ^ 0x5a)).toString("utf8"),
	};
}

const account = { id: "acct_1", email: "dev@example.com" };
let stateDir = "";

beforeEach(async () => {
	stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-account-"));
});
afterEach(async () => {
	await rm(stateDir, { recursive: true, force: true });
});

test("round-trips the account and refresh token", async () => {
	const safeStorage = fakeSafeStorage();
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_secret" });
	await expect(readStoredAccount(stateDir, safeStorage)).resolves.toEqual({ account, refreshToken: "rt_secret" });
});

test("the refresh token never appears on disk in plaintext, and the file is owner-only", async () => {
	const file = path.join(stateDir, AO_ACCOUNT_FILE_NAME);
	await writeStoredAccount(stateDir, fakeSafeStorage(), { account, refreshToken: "rt_secret" });

	const raw = await readFile(file, "utf8");
	expect(raw).not.toContain("rt_secret");
	// Identity stays readable so the signed-in row renders without a keychain unlock.
	expect(raw).toContain("dev@example.com");
	expect((await stat(file)).mode & 0o777).toBe(0o600);
});

// The refresh token rotates on use, so the copy a write replaces is already
// revoked. A temp file left behind, or reused because it happens to carry this
// pid, is an encrypted credential nobody owns.
test("leaves no temp file behind, and never names one after the pid", async () => {
	await writeStoredAccount(stateDir, fakeSafeStorage(), { account, refreshToken: "rt_secret" });
	await writeStoredAccount(stateDir, fakeSafeStorage(), { account, refreshToken: "rt_rotated" });

	expect(await readdir(stateDir)).toEqual([AO_ACCOUNT_FILE_NAME]);
	await expect(readFile(path.join(stateDir, `.ao-account-${process.pid}.json`), "utf8")).rejects.toThrow();
});

test("a failed write leaves nothing to clean up and does not damage the stored token", async () => {
	const safeStorage = fakeSafeStorage();
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_secret" });

	const exploding: SafeStorageLike = {
		...safeStorage,
		encryptString: () => {
			throw new Error("keychain locked");
		},
	};
	await expect(writeStoredAccount(stateDir, exploding, { account, refreshToken: "rt_rotated" })).rejects.toThrow();
	expect(await readdir(stateDir)).toEqual([AO_ACCOUNT_FILE_NAME]);
	await expect(readStoredAccount(stateDir, safeStorage)).resolves.toEqual({ account, refreshToken: "rt_secret" });
});

test("nothing at all is written when the OS has no credential store", async () => {
	await expect(
		writeStoredAccount(stateDir, fakeSafeStorage(false), { account, refreshToken: "rt_secret" }),
	).rejects.toThrow(SAFE_STORAGE_UNAVAILABLE_MESSAGE);
	await expect(readFile(path.join(stateDir, AO_ACCOUNT_FILE_NAME), "utf8")).rejects.toThrow();
});

test("an empty refresh token is refused rather than stored", async () => {
	await expect(writeStoredAccount(stateDir, fakeSafeStorage(), { account, refreshToken: "" })).rejects.toThrow(
		/empty AO refresh token/,
	);
});

test("no stored sign-in reads as null, not as an error", async () => {
	await expect(readStoredAccount(stateDir, fakeSafeStorage())).resolves.toBeNull();
});

test("a garbage or future-version file reads as no stored sign-in", async () => {
	const file = path.join(stateDir, AO_ACCOUNT_FILE_NAME);
	await writeFile(file, "{not json");
	await expect(readStoredAccount(stateDir, fakeSafeStorage())).resolves.toBeNull();
	await writeFile(file, JSON.stringify({ version: 99, accountId: "a", email: "e", refreshToken: "x" }));
	await expect(readStoredAccount(stateDir, fakeSafeStorage())).resolves.toBeNull();
});

test("a stored token that cannot be decrypted here fails loudly", async () => {
	await writeStoredAccount(stateDir, fakeSafeStorage(), { account, refreshToken: "rt_secret" });
	await expect(readStoredAccount(stateDir, fakeSafeStorage(false))).rejects.toThrow(SAFE_STORAGE_UNAVAILABLE_MESSAGE);

	const broken: SafeStorageLike = {
		isEncryptionAvailable: () => true,
		encryptString: () => Buffer.from(""),
		decryptString: () => {
			throw new Error("keychain says no");
		},
	};
	await expect(readStoredAccount(stateDir, broken)).rejects.toThrow(/could not be decrypted/);
});

test("sign-out deletes the token file, and is fine when there is none", async () => {
	const safeStorage = fakeSafeStorage();
	await writeStoredAccount(stateDir, safeStorage, { account, refreshToken: "rt_secret" });
	await clearStoredAccount(stateDir);
	await expect(readFile(path.join(stateDir, AO_ACCOUNT_FILE_NAME), "utf8")).rejects.toThrow();
	await expect(readStoredAccount(stateDir, safeStorage)).resolves.toBeNull();
	await expect(clearStoredAccount(stateDir)).resolves.toBeUndefined();
});
