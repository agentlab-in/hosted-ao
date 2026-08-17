import { randomBytes } from "node:crypto";
import { mkdir, open, readFile, rename, rm } from "node:fs/promises";
import path from "node:path";
import { LOCAL_MACHINE_ID, parseMachineOrigin, type AoMachine } from "../shared/ao-machines";
import type { SafeStorageLike } from "./ao-account-store";
import { createPairCertificateVerifyProc, type PairCertificateVerifyProc } from "./paired-machine-cert";
import { fetchWithDeadline } from "./request-deadline";

/**
 * The registry of paired boxes (docs/adr/0003-pair-mode-gateway.md) and their
 * pinned certificate fingerprints: the third machine origin, alongside local
 * and hosted, reusing `AoMachine` rather than a parallel shape.
 *
 * Everything here lives under the state root and never reaches the control
 * plane: pair mode's whole point is a box that needs no domain and no
 * account, so nothing about it is sent anywhere but this file on disk. The
 * passcode is encrypted with the same `safeStorage` mechanism
 * ao-account-store.ts already uses for the account refresh token.
 *
 * This module owns the data and the transport-level pin enforcement
 * (`verifyCertificate`, wired into `session.defaultSession` in main.ts). The
 * add-machine screen, the passcode entry form, and the fingerprint comparison
 * screen are a different task (batch 3, task 8); this module only exposes
 * what that UI needs through the preload bridge.
 */

/** File under the ~/.ao state dir, beside ao-account.json and ao-machine.json. */
export const AO_PAIRED_MACHINES_FILE_NAME = "ao-paired-machines.json";

const SCHEMA_VERSION = 1;

/** A probe attempt is denied by design (see paired-machine-cert.ts); this just
 * bounds how long the app waits for the TLS handshake that captures the
 * presented fingerprint before giving up. */
const DEFAULT_PROBE_TIMEOUT_MS = 8000;

export type PairedMachineRecord = {
	id: string;
	name: string;
	address: string;
	port: number;
	/** Null until the user has compared and accepted a fingerprint. */
	pinnedFingerprint: string | null;
	lastSeen: string | null;
};

type PersistedPairedMachine = PairedMachineRecord & {
	/** base64 of safeStorage.encryptString(passcode). */
	passcode: string;
};

type PairedMachinesFile = {
	version: number;
	machines: PersistedPairedMachine[];
};

export type PairFingerprintResult = { fingerprint: string } | { error: string };

export type PairedMachinesControllerDeps = {
	/** The ~/.ao state dir, or null when the home dir is unresolvable. */
	stateDir: string | null;
	safeStorage: SafeStorageLike;
	/** Electron's `net.fetch`, which runs through the session's network stack
	 * and therefore through `verifyCertificate` below. A plain global `fetch`
	 * would not: it bypasses the session entirely, which is exactly the gap
	 * this whole module exists to close. */
	netFetch?: typeof fetch;
	probeTimeoutMs?: number;
};

export type PairedMachinesController = {
	/** Load the registry off disk. Must resolve before `verifyCertificate` can
	 * see any pin: it runs synchronously off the in-memory cache this builds,
	 * because Electron calls it from the network service without awaiting it. */
	load: () => Promise<void>;
	list: () => AoMachine[];
	/**
	 * Add a newly paired machine, or update an existing one (matched by id) --
	 * a re-pair after a fingerprint mismatch is the same call with a fresh
	 * fingerprint and passcode. `fingerprint` must already be the value the
	 * user compared and accepted; this module never pins one on its own.
	 */
	add: (input: {
		id: string;
		name: string;
		address: string;
		port: number;
		passcode: string;
		fingerprint: string;
	}) => Promise<AoMachine>;
	remove: (id: string) => Promise<void>;
	/** Decrypted passcode for a paired machine, or null if it is not registered. */
	getPasscode: (id: string) => Promise<string | null>;
	touchLastSeen: (id: string) => Promise<void>;
	/**
	 * Attempt a connection to an address:port that is not (yet) a registered
	 * paired machine, so the pairing flow can show the presented fingerprint
	 * for comparison against what the box printed. The connection is always
	 * denied at the TLS layer (see paired-machine-cert.ts): this only ever
	 * captures what was presented, it never trusts it.
	 */
	probeFingerprint: (address: string, port: number) => Promise<PairFingerprintResult>;
	/**
	 * The fingerprint currently pinned for a registered machine id, or null if
	 * that id is not registered or has no pin yet. Read-only, synchronous off
	 * the in-memory registry: never mutates anything.
	 *
	 * What the add-machine flow (task 8) uses to tell a genuine re-pair (the
	 * box presenting the same fingerprint again, e.g. only the passcode
	 * changed) from an actual fingerprint mismatch, before it offers the user
	 * anything to accept. The fingerprint itself is not a secret -- it is
	 * printed on the box for exactly this comparison -- so handing it back is
	 * no different from what probeFingerprint already exposes.
	 */
	getPinnedFingerprint: (id: string) => string | null;
	/** Wire directly into `session.defaultSession.setCertificateVerifyProc`. */
	verifyCertificate: PairCertificateVerifyProc;
};

function filePath(stateDir: string): string {
	return path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME);
}

function validatePort(port: number): boolean {
	return Number.isInteger(port) && port > 0 && port <= 65535;
}

/** `AoMachine.baseUrl` for a paired machine, and the validation gate for its
 * address: reuses parseMachineOrigin rather than re-deriving the same rules
 * about scheme, path, and credentials. */
function baseUrlFor(address: string, port: number): string | null {
	return parseMachineOrigin(`https://${address}:${port}`);
}

async function readAll(stateDir: string): Promise<PersistedPairedMachine[]> {
	let raw: string;
	try {
		raw = await readFile(filePath(stateDir), "utf8");
	} catch (err) {
		const code = (err as NodeJS.ErrnoException).code;
		if (code === "ENOENT" || code === "ENOTDIR") return [];
		throw err;
	}
	let parsed: Partial<PairedMachinesFile>;
	try {
		parsed = JSON.parse(raw) as Partial<PairedMachinesFile>;
	} catch {
		return [];
	}
	if (parsed.version !== SCHEMA_VERSION || !Array.isArray(parsed.machines)) return [];
	return parsed.machines.filter((machine): machine is PersistedPairedMachine => {
		return (
			!!machine &&
			typeof machine.id === "string" &&
			!!machine.id &&
			typeof machine.address === "string" &&
			!!machine.address &&
			validatePort(machine.port) &&
			typeof machine.passcode === "string"
		);
	});
}

/** Atomic write, mirroring ao-account.json and ao-machine.json: a random-named
 * temp file in the same dir, fsync, then rename, so a reader never observes a
 * half-written registry and a crashed write cannot leave a stale temp with
 * predictable permissions. */
async function writeAll(stateDir: string, machines: PersistedPairedMachine[]): Promise<void> {
	const file: PairedMachinesFile = { version: SCHEMA_VERSION, machines };
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const tmp = path.join(stateDir, `.ao-paired-machines-${randomBytes(8).toString("hex")}.json`);
	try {
		const handle = await open(tmp, "wx", 0o600);
		try {
			await handle.writeFile(`${JSON.stringify(file, null, 2)}\n`);
			await handle.sync();
		} finally {
			await handle.close();
		}
		await rename(tmp, filePath(stateDir));
	} catch (err) {
		await rm(tmp, { force: true });
		throw err;
	}
}

function toAoMachine(record: PairedMachineRecord): AoMachine | null {
	const baseUrl = baseUrlFor(record.address, record.port);
	if (!baseUrl) return null;
	return {
		id: record.id,
		name: record.name || record.address,
		baseUrl,
		local: false,
		createdAt: null,
		lastSeen: record.lastSeen,
		reachability: "unknown",
		harness: "unknown",
		harnessCommand: null,
	};
}

export function createPairedMachinesController(deps: PairedMachinesControllerDeps): PairedMachinesController {
	const netFetch = deps.netFetch ?? fetch;
	const probeTimeoutMs = deps.probeTimeoutMs ?? DEFAULT_PROBE_TIMEOUT_MS;

	/** In-memory mirror of disk, keyed by id. verifyCertificate reads only the
	 * derived maps below, never this directly, and never touches disk: it must
	 * stay synchronous. */
	let records = new Map<string, PersistedPairedMachine>();
	/** hostname -> pinned fingerprint, for every record that has one. Rebuilt
	 * whenever `records` changes. */
	let pinnedByHost = new Map<string, string>();
	/** Hosts currently mid-probe for first-time pairing: not yet in `records`,
	 * but connections to them must still be intercepted so the presented
	 * fingerprint can be captured instead of falling through to default
	 * verification (which would fail anyway, and would capture nothing). */
	const pendingHosts = new Set<string>();
	/** Latest fingerprint actually presented per hostname, for any host
	 * `isPairHost` recognises. What `probeFingerprint` reads back. */
	const presented = new Map<string, string>();

	function rebuildPinnedByHost(): void {
		pinnedByHost = new Map(
			[...records.values()]
				.filter((record) => record.pinnedFingerprint)
				.map((record) => [record.address, record.pinnedFingerprint as string]),
		);
	}

	async function persist(): Promise<void> {
		if (!deps.stateDir) throw new Error("Could not resolve the ~/.ao state directory, so pairing cannot be saved.");
		await writeAll(deps.stateDir, [...records.values()]);
	}

	const verifyCertificate = createPairCertificateVerifyProc({
		isPairHost: (hostname) => pendingHosts.has(hostname) || pinnedByHost.has(hostname),
		getPinnedFingerprint: (hostname) => pinnedByHost.get(hostname) ?? null,
		onPresented: (hostname, fingerprint) => presented.set(hostname, fingerprint),
	});

	return {
		async load(): Promise<void> {
			if (!deps.stateDir) return;
			const rows = await readAll(deps.stateDir);
			records = new Map(rows.map((row) => [row.id, row]));
			rebuildPinnedByHost();
		},

		list(): AoMachine[] {
			return [...records.values()]
				.map((record) => toAoMachine(record))
				.filter((machine): machine is AoMachine => machine !== null);
		},

		async add(input): Promise<AoMachine> {
			if (!input.id || input.id === LOCAL_MACHINE_ID) throw new Error(`Not a usable machine id: ${input.id}`);
			if (!validatePort(input.port)) throw new Error(`Not a valid port: ${input.port}`);
			const baseUrl = baseUrlFor(input.address, input.port);
			if (!baseUrl) throw new Error(`Not a usable address: ${input.address}`);
			if (!input.fingerprint) throw new Error("Refusing to pair a machine with no accepted fingerprint.");
			if (!input.passcode) throw new Error("Refusing to store an empty passcode.");
			if (!deps.safeStorage.isEncryptionAvailable()) {
				throw new Error(
					"This system has no OS credential store available, so the paired machine's passcode cannot be " +
						"encrypted. The app will not write it to disk in plaintext.",
				);
			}
			const record: PersistedPairedMachine = {
				id: input.id,
				name: input.name || input.address,
				address: input.address,
				port: input.port,
				pinnedFingerprint: input.fingerprint,
				lastSeen: records.get(input.id)?.lastSeen ?? null,
				passcode: deps.safeStorage.encryptString(input.passcode).toString("base64"),
			};
			const previous = records;
			records = new Map(previous).set(record.id, record);
			rebuildPinnedByHost();
			try {
				await persist();
			} catch (err) {
				records = previous;
				rebuildPinnedByHost();
				throw err;
			}
			const machine = toAoMachine(record);
			if (!machine) throw new Error(`Not a usable address: ${input.address}`);
			return machine;
		},

		async remove(id: string): Promise<void> {
			if (!records.has(id)) return;
			const previous = records;
			records = new Map(previous);
			records.delete(id);
			rebuildPinnedByHost();
			try {
				await persist();
			} catch (err) {
				records = previous;
				rebuildPinnedByHost();
				throw err;
			}
		},

		async getPasscode(id: string): Promise<string | null> {
			const record = records.get(id);
			if (!record) return null;
			if (!deps.safeStorage.isEncryptionAvailable()) {
				throw new Error("This system has no OS credential store available, so the paired machine's passcode cannot be read.");
			}
			try {
				return deps.safeStorage.decryptString(Buffer.from(record.passcode, "base64"));
			} catch {
				throw new Error("This paired machine's stored passcode could not be decrypted on this machine. Re-pair it.");
			}
		},

		async touchLastSeen(id: string): Promise<void> {
			const record = records.get(id);
			if (!record) return;
			const previous = records;
			records = new Map(previous).set(id, { ...record, lastSeen: new Date().toISOString() });
			try {
				await persist();
			} catch (err) {
				records = previous;
				throw err;
			}
		},

		async probeFingerprint(address: string, port: number): Promise<PairFingerprintResult> {
			if (!validatePort(port)) return { error: `Not a valid port: ${port}` };
			const baseUrl = baseUrlFor(address, port);
			if (!baseUrl) return { error: `Not a usable address: ${address}` };

			presented.delete(address);
			pendingHosts.add(address);
			try {
				await fetchWithDeadline(netFetch, `${baseUrl}/`, { method: "GET", redirect: "manual" }, probeTimeoutMs, "Pairing probe");
			} catch {
				// Expected: an unpinned host is always denied at the TLS layer (see
				// paired-machine-cert.ts), and a box that is simply unreachable denies
				// nothing to capture at all. Either way, the fingerprint map below is
				// the source of truth for whether anything was actually presented.
			} finally {
				pendingHosts.delete(address);
			}

			const fingerprint = presented.get(address);
			return fingerprint
				? { fingerprint }
				: { error: "No certificate could be retrieved from that address. Check the address, port, and that the box is reachable." };
		},

		getPinnedFingerprint(id: string): string | null {
			return records.get(id)?.pinnedFingerprint ?? null;
		},

		verifyCertificate,
	};
}
