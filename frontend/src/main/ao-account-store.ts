import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import type { AoAccount } from "../shared/ao-account";

/**
 * The signed-in AO account plus its refresh token, on disk under the ~/.ao state
 * dir (never an OS default app-data location, see the hard rule in AGENTS.md).
 *
 * The refresh token is encrypted with Electron `safeStorage`, which is backed by
 * the macOS Keychain, libsecret on Linux, and DPAPI on Windows. The account id and
 * email are stored in the clear: they are identity, not a credential, and keeping
 * them readable means the signed-in row can render without a keychain unlock.
 */

/** File under the ~/.ao state dir, beside app-state.json and update-settings.json. */
export const AO_ACCOUNT_FILE_NAME = "ao-account.json";

const SCHEMA_VERSION = 1;

export const SAFE_STORAGE_UNAVAILABLE_MESSAGE =
	"This system has no OS credential store available, so the AO refresh token cannot be " +
	"encrypted. Signing in would mean writing a long-lived credential to disk in plaintext, " +
	"which the app will not do. On Linux, install and run a Secret Service provider (for " +
	"example gnome-keyring or kwallet), then restart the app. Local mode needs no account.";

/** The subset of Electron's safeStorage this module needs, so tests can fake it. */
export type SafeStorageLike = {
	isEncryptionAvailable: () => boolean;
	encryptString: (plainText: string) => Buffer;
	decryptString: (encrypted: Buffer) => string;
};

export type StoredAoAccount = {
	account: AoAccount;
	refreshToken: string;
};

type AccountFile = {
	version: number;
	accountId: string;
	email: string;
	/** base64 of safeStorage.encryptString(refreshToken). */
	refreshToken: string;
};

function accountFilePath(stateDir: string): string {
	return path.join(stateDir, AO_ACCOUNT_FILE_NAME);
}

/**
 * Read the stored sign-in. Returns null when there is nothing stored.
 *
 * Throws when a stored token exists but cannot be decrypted (no credential store,
 * or a keychain the OS re-keyed). The caller surfaces that as signed-out with the
 * reason instead of pretending the file is absent.
 */
export async function readStoredAccount(
	stateDir: string,
	safeStorage: SafeStorageLike,
): Promise<StoredAoAccount | null> {
	let raw: string;
	try {
		raw = await readFile(accountFilePath(stateDir), "utf8");
	} catch {
		return null;
	}

	let parsed: Partial<AccountFile>;
	try {
		parsed = JSON.parse(raw) as Partial<AccountFile>;
	} catch {
		return null;
	}
	if (parsed.version !== SCHEMA_VERSION) return null;
	const { accountId, email, refreshToken } = parsed;
	if (typeof accountId !== "string" || !accountId) return null;
	if (typeof email !== "string") return null;
	if (typeof refreshToken !== "string" || !refreshToken) return null;

	if (!safeStorage.isEncryptionAvailable()) throw new Error(SAFE_STORAGE_UNAVAILABLE_MESSAGE);

	let decrypted: string;
	try {
		decrypted = safeStorage.decryptString(Buffer.from(refreshToken, "base64"));
	} catch {
		throw new Error("The stored AO sign-in could not be decrypted on this machine. Sign in again.");
	}
	if (!decrypted) throw new Error("The stored AO sign-in was empty. Sign in again.");

	return { account: { id: accountId, email }, refreshToken: decrypted };
}

/**
 * Persist the account and its refresh token. Refuses to write anything when
 * encryption is unavailable, rather than falling back to plaintext.
 *
 * Also the seam the refresh exchange writes through: the refresh token rotates on
 * every use (controlplane/TOKEN_CONTRACT.md), so the replacement must land here or
 * the next refresh replays a revoked token.
 */
export async function writeStoredAccount(
	stateDir: string,
	safeStorage: SafeStorageLike,
	stored: StoredAoAccount,
): Promise<void> {
	if (!stored.refreshToken) throw new Error("Refusing to store an empty AO refresh token.");
	if (!safeStorage.isEncryptionAvailable()) throw new Error(SAFE_STORAGE_UNAVAILABLE_MESSAGE);

	const file: AccountFile = {
		version: SCHEMA_VERSION,
		accountId: stored.account.id,
		email: stored.account.email,
		refreshToken: safeStorage.encryptString(stored.refreshToken).toString("base64"),
	};

	// Atomic write, mirroring app-state.json: a temp file in the same dir then a
	// rename, so a reader never sees a half-written credential.
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const tmp = path.join(stateDir, `.ao-account-${process.pid}.json`);
	await writeFile(tmp, `${JSON.stringify(file, null, 2)}\n`, { mode: 0o600 });
	await rename(tmp, accountFilePath(stateDir));
}

/** Sign-out: delete the stored token. Absent file is success, not an error. */
export async function clearStoredAccount(stateDir: string): Promise<void> {
	await rm(accountFilePath(stateDir), { force: true });
}
