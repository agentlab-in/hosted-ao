import { LOCAL_MACHINE_ID, type AoMachine, type AoMachinesState } from "../shared/ao-machines";
import { parseRunFile } from "../shared/daemon-discovery";
import type { PeerProject, PeerSession, PeerWorkspacesResult } from "../shared/peer-workspaces";
import type { AoControlPlaneCredential } from "./ao-machines";
import {
	createMachineTokenSource,
	type MachineTokenSource,
	type MachineTokenSourceDeps,
} from "./ao-machine-token";
import { SIGNED_OUT_REASON } from "./machine-transport";

/**
 * Read-only "peer" workspaces: the projects and sessions of the daemon that is
 * NOT the app's active one, so the UI can list them alongside the active
 * daemon's without touching `machine-transport.ts`'s one target/one token
 * machinery at all.
 *
 * Peer, relative to `AoMachinesState.activeMachineId`:
 *   - active is this computer -> peer is the account's online registered
 *     machine, if the account has one and it is reachable.
 *   - active is a registered machine -> peer is this computer's local
 *     loopback daemon.
 *
 * A remote peer is reached with a second, independent machine-audience token
 * source (never `createMachineTransport`'s), Bearer-only: no cookie is
 * installed, because a peer never opens `/mux` or the SSE stream. A local peer
 * is reached unauthenticated, like every other loopback caller. Every failure
 * mode collapses to `{state:"unavailable", reason}`: this never throws across
 * IPC, and `reason` is always a fixed, human-readable string, never a token,
 * a cookie value, or a raw error message that could carry one.
 */

/** A dead or slow peer must not hang the UI; a few seconds is plenty for two loopback-scale GETs. */
const DEFAULT_TIMEOUT_MS = 5_000;

export type PeerWorkspacesDeps = {
	/** Current machine list and which one is active. No network; read from ao-machines.ts's cache. */
	getMachinesState: () => AoMachinesState;
	/** This process's one control-plane credential, or null when it cannot exist. See ao-machines.ts. */
	credential: () => AoControlPlaneCredential | null;
	/** Display name for machine zero, the same one the machines list already uses (e.g. "This Mac"). */
	localMachineName: string;
	/**
	 * This computer's own running.json contents, or null when it cannot be read
	 * (no file, no permission, home dir unresolvable). Never throws.
	 */
	readLocalRunFile: () => Promise<string | null>;
	fetchImpl?: typeof fetch;
	timeoutMs?: number;
	/** Test seam, so a controller can be driven without a real token endpoint. */
	createTokenSource?: (deps: MachineTokenSourceDeps) => MachineTokenSource;
};

export type PeerWorkspacesController = {
	get: () => Promise<PeerWorkspacesResult>;
};

function unavailable(reason: string): PeerWorkspacesResult {
	return { state: "unavailable", reason };
}

type RawSessionRow = {
	id?: unknown;
	projectId?: unknown;
	displayName?: unknown;
	status?: unknown;
	activity?: unknown;
	branch?: unknown;
	harness?: unknown;
	kind?: unknown;
	updatedAt?: unknown;
};

function parseSessionRow(raw: unknown): { projectId: string; session: PeerSession } | null {
	if (!raw || typeof raw !== "object") return null;
	const row = raw as RawSessionRow;
	const id = typeof row.id === "string" ? row.id.trim() : "";
	const projectId = typeof row.projectId === "string" ? row.projectId.trim() : "";
	if (!id || !projectId) return null;
	const status = typeof row.status === "string" && row.status ? row.status : "unknown";
	const displayName = typeof row.displayName === "string" ? row.displayName.trim() : "";
	const activityRow = row.activity as { state?: unknown } | null | undefined;
	const activity =
		activityRow && typeof activityRow === "object" && typeof activityRow.state === "string"
			? activityRow.state
			: undefined;
	const branch = typeof row.branch === "string" && row.branch ? row.branch : undefined;
	const harness = typeof row.harness === "string" && row.harness ? row.harness : undefined;
	const kind = typeof row.kind === "string" && row.kind ? row.kind : undefined;
	const updatedAt = typeof row.updatedAt === "string" && row.updatedAt ? row.updatedAt : undefined;
	return {
		projectId,
		session: { id, title: displayName || id, status, activity, branch, harness, kind, updatedAt },
	};
}

/**
 * Shape `GET /api/v1/projects` and `GET /api/v1/sessions` bodies into
 * PeerProject[], grouping sessions under their project. Unusable rows are
 * dropped rather than failing the whole list, matching parseMachinesResponse's
 * one-bad-row-does-not-hide-the-rest rule. A session whose project id is not
 * in the projects list (the two fetches are not transactional) still gets a
 * home rather than being dropped.
 */
export function buildPeerProjects(projectsBody: unknown, sessionsBody: unknown): PeerProject[] {
	const projectRows = (projectsBody as { projects?: unknown } | null | undefined)?.projects;
	const sessionRows = (sessionsBody as { sessions?: unknown } | null | undefined)?.sessions;

	const projects = new Map<string, PeerProject>();
	if (Array.isArray(projectRows)) {
		for (const raw of projectRows) {
			if (!raw || typeof raw !== "object") continue;
			const row = raw as { id?: unknown; name?: unknown };
			const id = typeof row.id === "string" ? row.id.trim() : "";
			if (!id) continue;
			const name = typeof row.name === "string" && row.name.trim() ? row.name.trim() : id;
			projects.set(id, { id, name, sessions: [] });
		}
	}
	if (Array.isArray(sessionRows)) {
		for (const raw of sessionRows) {
			const parsed = parseSessionRow(raw);
			if (!parsed) continue;
			let project = projects.get(parsed.projectId);
			if (!project) {
				project = { id: parsed.projectId, name: parsed.projectId, sessions: [] };
				projects.set(parsed.projectId, project);
			}
			project.sessions.push(parsed.session);
		}
	}
	return [...projects.values()];
}

export function createPeerWorkspacesController(deps: PeerWorkspacesDeps): PeerWorkspacesController {
	const fetchImpl = deps.fetchImpl ?? fetch;
	const timeoutMs = deps.timeoutMs ?? DEFAULT_TIMEOUT_MS;
	const createTokenSource = deps.createTokenSource ?? createMachineTokenSource;

	/** One token source per machine id, independent of createMachineTransport's. */
	const tokenSources = new Map<string, MachineTokenSource>();

	function tokenSourceFor(machineId: string, credential: AoControlPlaneCredential): MachineTokenSource {
		let source = tokenSources.get(machineId);
		if (!source) {
			source = createTokenSource({
				controlPlaneUrl: credential.controlPlaneUrl,
				machineId,
				controlToken: credential.token,
				fetchImpl,
			});
			tokenSources.set(machineId, source);
		}
		return source;
	}

	/**
	 * Drop a token source for any machine id no longer in the known machines
	 * list (deregistered, or this account signed into a different one). Without
	 * this, `tokenSources` only ever grows: an entry accumulates for every
	 * machine id this process has ever seen as a peer, for the life of the
	 * process. Mirrors machine-transport.ts, which drops its one token source
	 * on setMachine when the target changes.
	 */
	function evictDepartedTokenSources(machines: AoMachine[]): void {
		const known = new Set(machines.map((machine) => machine.id));
		for (const machineId of tokenSources.keys()) {
			if (!known.has(machineId)) tokenSources.delete(machineId);
		}
	}

	async function fetchJson(url: string, headers: Record<string, string>): Promise<unknown> {
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), timeoutMs);
		try {
			const response = await fetchImpl(url, { headers, signal: controller.signal });
			if (!response.ok) throw new Error(`peer answered ${response.status}`);
			return await response.json();
		} finally {
			clearTimeout(timer);
		}
	}

	async function fetchProjects(baseUrl: string, headers: Record<string, string>): Promise<PeerProject[]> {
		const [projectsBody, sessionsBody] = await Promise.all([
			fetchJson(`${baseUrl}/api/v1/projects`, headers),
			fetchJson(`${baseUrl}/api/v1/sessions`, headers),
		]);
		return buildPeerProjects(projectsBody, sessionsBody);
	}

	async function remotePeer(machine: AoMachine): Promise<PeerWorkspacesResult> {
		const credential = deps.credential();
		if (!credential) return unavailable(SIGNED_OUT_REASON);

		let token: string | null;
		try {
			token = (await tokenSourceFor(machine.id, credential).get())?.token ?? null;
		} catch {
			return unavailable(`Could not sign in to ${machine.name}.`);
		}
		if (!token) return unavailable(SIGNED_OUT_REASON);

		try {
			const projects = await fetchProjects(machine.baseUrl, {
				Authorization: `Bearer ${token}`,
				Accept: "application/json",
			});
			return { state: "ok", machineId: machine.id, machineName: machine.name, isRemote: true, projects };
		} catch {
			return unavailable(`${machine.name} is not reachable.`);
		}
	}

	async function localPeer(): Promise<PeerWorkspacesResult> {
		let contents: string | null;
		try {
			contents = await deps.readLocalRunFile();
		} catch {
			contents = null;
		}
		const info = contents ? parseRunFile(contents) : null;
		if (!info) return unavailable("This computer's local AO daemon is not running.");

		try {
			const projects = await fetchProjects(`http://127.0.0.1:${info.port}`, { Accept: "application/json" });
			return {
				state: "ok",
				machineId: LOCAL_MACHINE_ID,
				machineName: deps.localMachineName,
				isRemote: false,
				projects,
			};
		} catch {
			return unavailable("This computer's local AO daemon is not reachable.");
		}
	}

	return {
		async get(): Promise<PeerWorkspacesResult> {
			const machinesState = deps.getMachinesState();
			evictDepartedTokenSources(machinesState.machines);
			if (machinesState.activeMachineId !== LOCAL_MACHINE_ID) {
				// A registered machine is active, so the peer is this computer.
				return localPeer();
			}
			const peer = machinesState.machines.find((machine) => !machine.local && machine.reachability === "online");
			if (!peer) return unavailable("No other machine is online for this account.");
			return remotePeer(peer);
		},
	};
}
