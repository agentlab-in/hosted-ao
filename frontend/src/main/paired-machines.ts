import { randomBytes } from "node:crypto";
import { mkdir, open, readFile, rename, rm } from "node:fs/promises";
import path from "node:path";
import {
	harnessFromDoctorChecks,
	LOCAL_MACHINE_ID,
	parseMachineOrigin,
	type AoMachine,
	type AoMachineHarness,
	type AoMachineReachability,
} from "../shared/ao-machines";
export type SafeStorageLike = {
	isEncryptionAvailable: () => boolean;
	encryptString: (plainText: string) => Buffer;
	decryptString: (encrypted: Buffer) => string;
};
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

const SCHEMA_VERSION = 2;

/** A probe attempt is denied by design (see paired-machine-cert.ts); this just
 * bounds how long the app waits for the TLS handshake that captures the
 * presented fingerprint before giving up. */
const DEFAULT_PROBE_TIMEOUT_MS = 8000;

export type PairedMachineRecord = {
	id: string;
	name: string;
	address: string;
	port: number;
	/**
	 * Ordered address hints as "host:port" strings (IPv6 hosts bracketed, the
	 * same grammar pair-string.ts parses), first entry is the last-known-good
	 * address. `address`/`port` above always track this entry's host and port
	 * so `pairedMachineId`, `baseUrlFor`, and everything else built on a single
	 * address keep working unchanged; `promoteAddress` is what keeps the two in
	 * sync when the winning hint changes.
	 */
	addresses: string[];
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
	 * this whole module exists to close. Used for every authenticated call
	 * (`refresh`'s doctor probe) against a host that is expected to already be
	 * pinned. */
	netFetch?: typeof fetch;
	/**
	 * Electron's `net.fetch` bound to a throwaway session, used only by
	 * `probeFingerprint`. `probeFingerprint`'s whole point is a connection that
	 * is *always* denied at the TLS layer (see paired-machine-cert.ts: an
	 * unpinned host has no accept path), and Chromium caches that per-host
	 * certificate-error verdict at the network-service level for the life of
	 * the session -- below where `verifyCertificate` runs, so the verify proc
	 * is not even re-consulted once the cache is warm. If the capture probe
	 * shared `netFetch`'s session, pinning the host in `add()` would not undo
	 * that cached rejection: the very next request against the pinned host
	 * would still be short-circuited to a failure, reading as "unreachable"
	 * until the app restarts and the cache is gone. Routing the capture probe
	 * through a session that is discarded afterward, instead of the one real
	 * traffic uses, means the poisoned verdict dies with it. Defaults to
	 * `netFetch` when omitted (tests construct one shared fetch and do not
	 * exercise this failure mode).
	 */
	probeNetFetch?: typeof fetch;
	probeTimeoutMs?: number;
};

/** Readiness fields from a doctor report; local mirror of the same fields on
 * `AoMachine` so this module does not need ao-machines.ts's unexported type. */
type HarnessReadiness = { harness: AoMachineHarness; harnessCommand: string | null };
const HARNESS_UNKNOWN: HarnessReadiness = { harness: "unknown", harnessCommand: null };

export type PairedMachinesController = {
	/** Load the registry off disk. Must resolve before `verifyCertificate` can
	 * see any pin: it runs synchronously off the in-memory cache this builds,
	 * because Electron calls it from the network service without awaiting it. */
	load: () => Promise<void>;
	list: () => AoMachine[];
	/**
	 * Probe every registered machine's reachability and agent-harness readiness
	 * via `GET /api/v1/doctor`, authenticated with its own stored passcode --
	 * the same route and the same doctor checks `ao-machines.ts`'s hosted
	 * machines would read if the daemon route existed for them yet (see
	 * `harnessFromDoctorChecks`), reused here because pair mode's gateway
	 * already proxies it. Any answer, even a non-2xx one, means the box is up;
	 * only a transport failure or a timeout counts as offline, mirroring
	 * ao-machines.ts's own liveness probe. Returns the freshly probed list.
	 */
	refresh: () => Promise<AoMachine[]>;
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
		/**
		 * Ordered address hints for this machine, as produced by a parsed
		 * `ao-pair://` string with the winning candidate promoted to the front
		 * (see pair-string.ts). Defaults to the single `address:port` hint when
		 * omitted, matching what a v1-shaped caller always implied.
		 */
		addresses?: string[];
	}) => Promise<AoMachine>;
	remove: (id: string) => Promise<void>;
	/** Decrypted passcode for a paired machine, or null if it is not registered. */
	getPasscode: (id: string) => Promise<string | null>;
	touchLastSeen: (id: string) => Promise<void>;
	/** Ordered address hints for a registered machine, or an empty array if it
	 * is not registered. Read-only, synchronous off the in-memory registry. */
	getAddresses: (id: string) => string[];
	/**
	 * Promote a hint to the front of a machine's address list on a successful
	 * connect, and make it the current `address`/`port` too, so a future
	 * candidate race starts from the address that actually worked last time.
	 * A hint not already in the list is added at the front rather than
	 * rejected, since a box's address can legitimately change between races.
	 * No-op for an unregistered id.
	 */
	promoteAddress: (id: string, addr: string) => Promise<void>;
	/**
	 * Attempt a connection to an address:port that is not (yet) a registered
	 * paired machine, so the pairing flow can show the presented fingerprint
	 * for comparison against what the box printed. The connection is always
	 * denied at the TLS layer (see paired-machine-cert.ts): this only ever
	 * captures what was presented, it never trusts it. Runs over
	 * `probeNetFetch`, not `netFetch`: see that dep's doc comment for why the
	 * always-denied capture connection must not share a session with real
	 * traffic to a pinned host.
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
 * about scheme, path, and credentials. Brackets a bare IPv6 host (stored
 * unbracketed, the same grammar pair-string.ts's `PairAddr.host` uses) so
 * `new URL()` inside parseMachineOrigin does not throw on the bare colons. */
function baseUrlFor(address: string, port: number): string | null {
	const host = address.includes(":") ? `[${address}]` : address;
	return parseMachineOrigin(`https://${host}:${port}`);
}

/** "host:port", bracketing an IPv6 host the way pair-string.ts's grammar
 * requires, so a hint round-trips through parsePairString unchanged. */
function formatAddress(host: string, port: number): string {
	return host.includes(":") ? `[${host}]:${port}` : `${host}:${port}`;
}

/** Inverse of formatAddress. Returns null for anything that is not a
 * well-formed "host:port" hint (used defensively in promoteAddress, since a
 * hint's origin -- ultimately a parsed pairing string -- is outside this
 * module's control). */
function splitAddress(addr: string): { host: string; port: number } | null {
	let host: string;
	let portStr: string;
	if (addr.startsWith("[")) {
		const closeIdx = addr.indexOf("]");
		if (closeIdx === -1 || addr[closeIdx + 1] !== ":") return null;
		host = addr.slice(1, closeIdx);
		portStr = addr.slice(closeIdx + 2);
	} else {
		const colonIdx = addr.lastIndexOf(":");
		if (colonIdx === -1) return null;
		host = addr.slice(0, colonIdx);
		portStr = addr.slice(colonIdx + 1);
		if (host.includes(":")) return null;
	}
	if (!host || !/^\d+$/.test(portStr)) return null;
	const port = Number(portStr);
	return validatePort(port) ? { host, port } : null;
}

/** Fields shared by every schema version this module has ever written; a v1
 * record is exactly this shape, with no `addresses`. */
type PersistedPairedMachineCore = Omit<PersistedPairedMachine, "addresses">;

function hasCoreFields(machine: unknown): machine is PersistedPairedMachineCore {
	const m = machine as Partial<PersistedPairedMachineCore> | null | undefined;
	return (
		!!m &&
		typeof m.id === "string" &&
		!!m.id &&
		typeof m.address === "string" &&
		!!m.address &&
		validatePort(m.port as number) &&
		typeof m.passcode === "string"
	);
}

function hasAddressHints(machine: PersistedPairedMachineCore): machine is PersistedPairedMachine {
	const addresses = (machine as PersistedPairedMachine).addresses;
	return Array.isArray(addresses) && addresses.length > 0 && addresses.every((a) => typeof a === "string" && !!a);
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
	if (!Array.isArray(parsed.machines)) return [];
	const core = parsed.machines.filter(hasCoreFields);

	// v1 predates `addresses`: synthesize the single hint it always implied.
	if (parsed.version === 1) {
		return core.map((machine) => ({ ...machine, addresses: [formatAddress(machine.address, machine.port)] }));
	}
	if (parsed.version !== SCHEMA_VERSION) return [];
	return core.filter(hasAddressHints);
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

function toAoMachine(
	record: PairedMachineRecord,
	reachability: AoMachineReachability,
	harness: HarnessReadiness,
): AoMachine | null {
	const baseUrl = baseUrlFor(record.address, record.port);
	if (!baseUrl) return null;
	return {
		id: record.id,
		name: record.name || record.address,
		baseUrl,
		local: false,
		createdAt: null,
		lastSeen: record.lastSeen,
		reachability,
		...harness,
	};
}

export function createPairedMachinesController(deps: PairedMachinesControllerDeps): PairedMachinesController {
	const netFetch = deps.netFetch ?? fetch;
	const probeNetFetch = deps.probeNetFetch ?? netFetch;
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
	/** Reachability and harness readiness from the last `refresh()`, by machine
	 * id. Ephemeral like ao-machines.ts's own reachability: never persisted,
	 * defaults to "unknown" for a machine that has never been probed. */
	let reachabilityById = new Map<string, AoMachineReachability>();
	let harnessById = new Map<string, HarnessReadiness>();

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

	/** Shared by the public `getPasscode` and the reachability probe below, so
	 * there is exactly one place that decrypts a stored passcode. */
	async function decryptPasscode(record: PersistedPairedMachine): Promise<string | null> {
		if (!deps.safeStorage.isEncryptionAvailable()) {
			throw new Error("This system has no OS credential store available, so the paired machine's passcode cannot be read.");
		}
		try {
			return deps.safeStorage.decryptString(Buffer.from(record.passcode, "base64"));
		} catch {
			throw new Error("This paired machine's stored passcode could not be decrypted on this machine. Re-pair it.");
		}
	}

	/** One machine's doctor probe: any HTTP answer at all (see the `refresh`
	 * doc comment) means the box is up. Never throws; a passcode that cannot be
	 * decrypted or a bad credential just reads as unreachable, since nothing
	 * useful can be said about a box this process cannot authenticate to. */
	async function probeReachability(
		record: PersistedPairedMachine,
	): Promise<{ reachability: AoMachineReachability; harness: HarnessReadiness; answered: boolean }> {
		const baseUrl = baseUrlFor(record.address, record.port);
		if (!baseUrl) return { reachability: "offline", harness: HARNESS_UNKNOWN, answered: false };
		let passcode: string | null;
		try {
			passcode = await decryptPasscode(record);
		} catch {
			return { reachability: "offline", harness: HARNESS_UNKNOWN, answered: false };
		}
		if (!passcode) return { reachability: "offline", harness: HARNESS_UNKNOWN, answered: false };
		try {
			const response = await fetchWithDeadline(
				netFetch,
				`${baseUrl}/api/v1/doctor`,
				{ method: "GET", headers: { Authorization: `Bearer ${passcode}` } },
				probeTimeoutMs,
				"Paired machine doctor probe",
			);
			if (!response.ok) return { reachability: "online", harness: HARNESS_UNKNOWN, answered: true };
			return { reachability: "online", harness: harnessFromDoctorChecks(await response.json()), answered: true };
		} catch {
			// Transport failure or timeout: the only case that is actually offline,
			// mirroring ao-machines.ts's own probe() (see its doc comment).
			return { reachability: "offline", harness: HARNESS_UNKNOWN, answered: false };
		}
	}

	const verifyCertificate = createPairCertificateVerifyProc({
		isPairHost: (hostname) => pendingHosts.has(hostname) || pinnedByHost.has(hostname),
		getPinnedFingerprint: (hostname) => pinnedByHost.get(hostname) ?? null,
		onPresented: (hostname, fingerprint) => presented.set(hostname, fingerprint),
	});

	function list(): AoMachine[] {
		return [...records.values()]
			.map((record) =>
				toAoMachine(record, reachabilityById.get(record.id) ?? "unknown", harnessById.get(record.id) ?? HARNESS_UNKNOWN),
			)
			.filter((machine): machine is AoMachine => machine !== null);
	}

	return {
		async load(): Promise<void> {
			if (!deps.stateDir) return;
			const rows = await readAll(deps.stateDir);
			records = new Map(rows.map((row) => [row.id, row]));
			rebuildPinnedByHost();
		},

		list,

		async refresh(): Promise<AoMachine[]> {
			const current = [...records.values()];
			await Promise.all(
				current.map(async (record) => {
					const probe = await probeReachability(record);
					reachabilityById.set(record.id, probe.reachability);
					harnessById.set(record.id, probe.harness);
					if (probe.answered) {
						const previous = records;
						records = new Map(previous).set(record.id, { ...record, lastSeen: new Date().toISOString() });
						try {
							await persist();
						} catch (err) {
							records = previous;
							throw err;
						}
					}
				}),
			);
			return list();
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
				addresses: input.addresses && input.addresses.length > 0 ? input.addresses : [formatAddress(input.address, input.port)],
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
			// A re-pair may point at a different address or hand out a different
			// passcode; a reachability verdict from before either changed is stale.
			reachabilityById.delete(record.id);
			harnessById.delete(record.id);
			const machine = toAoMachine(record, "unknown", HARNESS_UNKNOWN);
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
			reachabilityById.delete(id);
			harnessById.delete(id);
		},

		async getPasscode(id: string): Promise<string | null> {
			const record = records.get(id);
			if (!record) return null;
			return decryptPasscode(record);
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

		getAddresses(id: string): string[] {
			return records.get(id)?.addresses ?? [];
		},

		async promoteAddress(id: string, addr: string): Promise<void> {
			const record = records.get(id);
			if (!record) return;
			const parsed = splitAddress(addr);
			if (!parsed) throw new Error(`Not a usable address: ${addr}`);
			const addresses = [addr, ...record.addresses.filter((existing) => existing !== addr)];
			const updated: PersistedPairedMachine = { ...record, address: parsed.host, port: parsed.port, addresses };
			const previous = records;
			records = new Map(previous).set(id, updated);
			rebuildPinnedByHost();
			try {
				await persist();
			} catch (err) {
				records = previous;
				rebuildPinnedByHost();
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
				await fetchWithDeadline(probeNetFetch, `${baseUrl}/`, { method: "GET", redirect: "manual" }, probeTimeoutMs, "Pairing probe");
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
