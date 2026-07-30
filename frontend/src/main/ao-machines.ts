import { randomBytes } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import {
	LOCAL_MACHINE_ID,
	localMachine,
	parseMachineOrigin,
	parseMachinesResponse,
	type AoMachine,
	type AoMachineReachability,
	type AoMachinesState,
	type AoMachinesStatus,
} from "../shared/ao-machines";
import { readControlPlaneUrl } from "../shared/control-plane";
import { createControlPlaneTokenSource, type ControlPlaneTokenSource } from "./ao-control-token";
import type { SafeStorageLike } from "./ao-account-store";

/**
 * Main-process owner of the machine list and of which machine is active.
 *
 * One machine is active at a time. Switching re-points the renderer at that
 * machine's base URL; running two machines side by side is out of scope and
 * nothing here is built toward it.
 *
 * The local machine is machine zero and is always in the list, signed in or
 * not. Nothing in this module gates it behind an account.
 *
 * The credential for the list is a control-plane-audience access token, from
 * ao-control-token.ts. See controlplane/TOKEN_CONTRACT.md, "The two audiences".
 */

/** Active machine, beside the other ~/.ao state files. Absent means local. */
export const AO_MACHINE_FILE_NAME = "ao-machine.json";

const SCHEMA_VERSION = 1;

/** A machine that has not answered within this is reported offline. */
const DEFAULT_PROBE_TIMEOUT_MS = 4000;

/**
 * What is persisted about the active machine.
 *
 * The whole record is stored rather than just the id, and that is load
 * bearing. At the next launch the app must know it is pointed at a remote
 * machine BEFORE it can list machines, because listing needs a token exchange
 * over the network. With only an id, that window would look exactly like "no
 * machine is active", and the app would spawn a local daemon for a remote
 * machine. See restore() and the lifecycle wiring in main.ts.
 */
type PersistedMachine = {
	id: string;
	name: string;
	baseUrl: string;
	lastSeen: string | null;
};

type MachineFile = {
	version: number;
	machine: PersistedMachine | null;
};

export type AoMachinesControllerDeps = {
	/** The ~/.ao state dir, or null when the home dir is unresolvable. */
	stateDir: string | null;
	env: Record<string, string | undefined>;
	safeStorage: SafeStorageLike;
	/** Display name for machine zero, for example "This Mac". */
	localMachineName: string;
	/**
	 * Called whenever the active machine changes, with null for the local
	 * machine. The main process turns this into the renderer's base URL.
	 */
	onActiveChange: (machine: AoMachine | null) => void;
	fetchImpl?: typeof fetch;
	probeTimeoutMs?: number;
	/** Test seam, so a controller can be driven without a real token exchange. */
	tokenSource?: ControlPlaneTokenSource;
};

export type AoMachinesController = {
	/** Current known state. No network: the renderer can poll this freely. */
	getState: () => AoMachinesState;
	/** List from the control plane and probe every machine's reachability. */
	refresh: () => Promise<AoMachinesState>;
	/** Make one machine active. Unknown ids are rejected rather than guessed. */
	select: (machineId: string) => Promise<AoMachinesState>;
	/** Boot: re-apply the machine chosen in a previous run, before any daemon start. */
	restore: () => Promise<AoMachine | null>;
	/** Sign-out: forget the token and fall back to the local machine. */
	reset: () => Promise<void>;
};

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function machineFilePath(stateDir: string): string {
	return path.join(stateDir, AO_MACHINE_FILE_NAME);
}

async function readPersisted(stateDir: string): Promise<PersistedMachine | null> {
	let raw: string;
	try {
		raw = await readFile(machineFilePath(stateDir), "utf8");
	} catch {
		return null;
	}
	let parsed: Partial<MachineFile>;
	try {
		parsed = JSON.parse(raw) as Partial<MachineFile>;
	} catch {
		return null;
	}
	if (parsed.version !== SCHEMA_VERSION || !parsed.machine) return null;
	const { id, name, baseUrl, lastSeen } = parsed.machine;
	const origin = parseMachineOrigin(baseUrl);
	if (typeof id !== "string" || !id || id === LOCAL_MACHINE_ID || !origin) return null;
	return {
		id,
		name: typeof name === "string" && name ? name : origin,
		baseUrl: origin,
		lastSeen: typeof lastSeen === "string" ? lastSeen : null,
	};
}

async function writePersisted(stateDir: string, machine: PersistedMachine | null): Promise<void> {
	if (!machine) {
		// Local is the absence of a file, so a fresh install and a switch back to
		// this computer leave exactly the same state on disk.
		await rm(machineFilePath(stateDir), { force: true });
		return;
	}
	const file: MachineFile = { version: SCHEMA_VERSION, machine };
	// Atomic write, mirroring app-state.json and ao-account.json.
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	// Random, not the pid: `mode` applies only on create, so a stale temp from a
	// crashed run with the same pid would be reused with whatever mode it had.
	const tmp = path.join(stateDir, `.ao-machine-${randomBytes(8).toString("hex")}.json`);
	await writeFile(tmp, `${JSON.stringify(file, null, 2)}\n`, { mode: 0o600 });
	await rename(tmp, machineFilePath(stateDir));
}

/** The remembered machine as a list entry, before the control plane is reached. */
function machineFromPersisted(persisted: PersistedMachine): AoMachine {
	return {
		id: persisted.id,
		name: persisted.name,
		baseUrl: persisted.baseUrl,
		local: false,
		createdAt: null,
		lastSeen: persisted.lastSeen,
		reachability: "unknown",
		harness: "unknown",
		harnessCommand: null,
	};
}

export function createAoMachinesController(deps: AoMachinesControllerDeps): AoMachinesController {
	const fetchImpl = deps.fetchImpl ?? fetch;
	const probeTimeoutMs = deps.probeTimeoutMs ?? DEFAULT_PROBE_TIMEOUT_MS;

	// Resolved once, like the account controller: a bad AO_CONTROL_URL must be
	// visible rather than silently falling back to the production control plane.
	let configError: string | null = null;
	let controlPlaneUrl = "";
	try {
		controlPlaneUrl = readControlPlaneUrl(deps.env);
	} catch (err) {
		configError = errorMessage(err);
	}

	const tokenSource =
		deps.tokenSource ??
		(deps.stateDir && !configError
			? createControlPlaneTokenSource({
					stateDir: deps.stateDir,
					controlPlaneUrl,
					safeStorage: deps.safeStorage,
					fetchImpl,
				})
			: null);

	/** Registered machines from the last successful list, or the remembered one. */
	let remote: AoMachine[] = [];
	let active: AoMachine | null = null;
	let status: AoMachinesStatus = "signed-out";
	let lastError: string | null = null;

	/**
	 * Bumped by anything that decides what is active out of band, which today is
	 * sign-out. A refresh that was already past `tokenSource.get()` when the user
	 * signed out still holds a usable token and would otherwise finish by writing
	 * ao-machine.json for an install that has no account left.
	 */
	let generation = 0;

	function currentState(): AoMachinesState {
		const local = localMachine(deps.localMachineName);
		return {
			status,
			machines: [local, ...remote],
			activeMachineId: active?.id ?? LOCAL_MACHINE_ID,
			...(lastError ? { error: lastError } : {}),
		};
	}

	async function setActive(machine: AoMachine | null): Promise<void> {
		const sameMachine = (active?.id ?? null) === (machine?.id ?? null);
		const sameUrl = (active?.baseUrl ?? "") === (machine?.baseUrl ?? "");
		active = machine;
		if (deps.stateDir) {
			await writePersisted(
				deps.stateDir,
				machine ? { id: machine.id, name: machine.name, baseUrl: machine.baseUrl, lastSeen: machine.lastSeen } : null,
			);
		}
		// Re-point the renderer only when the target actually moved, so a periodic
		// refresh does not churn every long-lived connection.
		if (!sameMachine || !sameUrl) deps.onActiveChange(machine);
	}

	async function fetchMachines(token: string): Promise<AoMachine[]> {
		const response = await fetchImpl(`${controlPlaneUrl}/api/v1/machines`, {
			headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
		});
		if (response.status === 401) {
			throw new Error("The control plane rejected this computer's sign-in. Sign in again.");
		}
		if (!response.ok) {
			throw new Error(`The control plane returned ${response.status} listing machines.`);
		}
		return parseMachinesResponse(await response.json());
	}

	/**
	 * Liveness probe, with no credential of any kind.
	 *
	 * The gateway answers an unauthenticated request for a route it does not
	 * proxy with a 404 before auth runs, so any HTTP answer at all proves the
	 * machine is up. Only a transport failure or a timeout is offline. Nothing
	 * about this result can reach the local daemon path: an offline machine is a
	 * status, not an absence, so the remote lifecycle still owns the app's
	 * daemon state and the local start function is never called. See main.ts.
	 */
	async function probe(machine: AoMachine): Promise<AoMachineReachability> {
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), probeTimeoutMs);
		try {
			await fetchImpl(`${machine.baseUrl}/`, { method: "GET", signal: controller.signal, redirect: "manual" });
			return "online";
		} catch {
			return "offline";
		} finally {
			clearTimeout(timer);
		}
	}

	async function probeAll(superseded: () => boolean): Promise<void> {
		const probed = remote;
		const results = await Promise.all(probed.map(probe));
		if (superseded()) return;
		remote = probed.map((machine, index) => ({ ...machine, reachability: results[index] }));
		if (!active) return;
		const refreshed = remote.find((machine) => machine.id === active?.id);
		if (!refreshed) return;
		// Reachability is the whole reason the renderer is or is not pointed at
		// this machine, so a change in it is a change of target.
		const moved = refreshed.reachability !== active.reachability;
		active = refreshed;
		if (moved) deps.onActiveChange(refreshed);
	}

	async function refresh(): Promise<AoMachinesState> {
		const startedAt = generation;
		const superseded = (): boolean => generation !== startedAt;

		if (configError) {
			status = "error";
			lastError = configError;
			return currentState();
		}
		if (!deps.stateDir || !tokenSource) {
			status = "error";
			lastError = "Could not resolve the ~/.ao state directory, so machines cannot be listed.";
			return currentState();
		}

		lastError = null;
		try {
			const token = await tokenSource.get();
			// Sign-out landed while this was in flight. Its answer describes an account
			// that is gone, so it is dropped rather than written over the reset.
			if (superseded()) return currentState();
			if (!token) {
				// Signed out. There is no way to reach a registered machine without an
				// account, so fall back to this computer, which needs none.
				status = "signed-out";
				remote = [];
				await setActive(null);
				return currentState();
			}
			const listed = await fetchMachines(token);
			if (superseded()) return currentState();
			remote = listed;
			status = "ready";
			const stillRegistered = active ? (remote.find((machine) => machine.id === active?.id) ?? null) : null;
			// A revoked machine is absent from the list. Falling back to this
			// computer beats staying pointed at something the account no longer owns.
			if (active && !stillRegistered) await setActive(null);
			else if (stillRegistered) await setActive(stillRegistered);
		} catch (err) {
			if (superseded()) return currentState();
			// The control plane is unreachable or refused the token. Keep the machine
			// that is already active and let the probe below say whether it is up:
			// a control-plane outage does not make a working machine unusable.
			status = "error";
			lastError = errorMessage(err);
			if (active && !remote.some((machine) => machine.id === active?.id)) remote = [active];
		}

		await probeAll(superseded);
		return currentState();
	}

	return {
		getState: currentState,
		refresh,

		async select(machineId: string): Promise<AoMachinesState> {
			if (machineId === LOCAL_MACHINE_ID) {
				await setActive(null);
				return currentState();
			}
			const machine = remote.find((candidate) => candidate.id === machineId);
			if (!machine) {
				lastError = "That machine is no longer in this account's list.";
				return currentState();
			}
			await setActive(machine);
			return currentState();
		},

		async restore(): Promise<AoMachine | null> {
			if (!deps.stateDir) return null;
			const persisted = await readPersisted(deps.stateDir);
			if (!persisted) return null;
			const machine = machineFromPersisted(persisted);
			remote = [machine];
			active = machine;
			deps.onActiveChange(machine);
			return machine;
		},

		async reset(): Promise<void> {
			generation += 1;
			tokenSource?.clear();
			remote = [];
			status = "signed-out";
			lastError = null;
			await setActive(null);
		},
	};
}
