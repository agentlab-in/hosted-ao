import { LOCAL_MACHINE_ID, type AoMachine, type AoMachinesState } from "../shared/ao-machines";
import { parseRunFile } from "../shared/daemon-discovery";
import type { PeerProject, PeerSession, PeerWorkspacesResult } from "../shared/peer-workspaces";

const DEFAULT_TIMEOUT_MS = 5_000;

export type PeerWorkspacesDeps = {
	getMachinesState: () => AoMachinesState;
	getPasscode: (machineId: string) => Promise<string | null>;
	localMachineName: string;
	readLocalRunFile: () => Promise<string | null>;
	fetchImpl?: typeof fetch;
	timeoutMs?: number;
};

export type PeerWorkspacesController = { get: () => Promise<PeerWorkspacesResult> };
const unavailable = (reason: string): PeerWorkspacesResult => ({ state: "unavailable", reason });

function parseSessionRow(raw: unknown): { projectId: string; session: PeerSession } | null {
	if (!raw || typeof raw !== "object") return null;
	const row = raw as Record<string, unknown>;
	const id = typeof row.id === "string" ? row.id.trim() : "";
	const projectId = typeof row.projectId === "string" ? row.projectId.trim() : "";
	if (!id || !projectId) return null;
	const activityRow = row.activity as { state?: unknown } | null | undefined;
	return { projectId, session: {
		id,
		title: typeof row.displayName === "string" && row.displayName.trim() ? row.displayName.trim() : id,
		status: typeof row.status === "string" && row.status ? row.status : "unknown",
		activity: activityRow && typeof activityRow.state === "string" ? activityRow.state : undefined,
		branch: typeof row.branch === "string" && row.branch ? row.branch : undefined,
		harness: typeof row.harness === "string" && row.harness ? row.harness : undefined,
		kind: typeof row.kind === "string" && row.kind ? row.kind : undefined,
		updatedAt: typeof row.updatedAt === "string" && row.updatedAt ? row.updatedAt : undefined,
	} };
}

export function buildPeerProjects(projectsBody: unknown, sessionsBody: unknown): PeerProject[] {
	const projects = new Map<string, PeerProject>();
	const projectRows = (projectsBody as { projects?: unknown } | null)?.projects;
	if (Array.isArray(projectRows)) for (const raw of projectRows) {
		if (!raw || typeof raw !== "object") continue;
		const row = raw as Record<string, unknown>;
		const id = typeof row.id === "string" ? row.id.trim() : "";
		if (id) projects.set(id, { id, name: typeof row.name === "string" && row.name.trim() ? row.name.trim() : id, sessions: [] });
	}
	const sessionRows = (sessionsBody as { sessions?: unknown } | null)?.sessions;
	if (Array.isArray(sessionRows)) for (const raw of sessionRows) {
		const parsed = parseSessionRow(raw);
		if (!parsed) continue;
		let project = projects.get(parsed.projectId);
		if (!project) {
			project = { id: parsed.projectId, name: parsed.projectId, sessions: [] };
			projects.set(parsed.projectId, project);
		}
		project.sessions.push(parsed.session);
	}
	return [...projects.values()];
}

export function createPeerWorkspacesController(deps: PeerWorkspacesDeps): PeerWorkspacesController {
	const fetchImpl = deps.fetchImpl ?? fetch;
	const timeoutMs = deps.timeoutMs ?? DEFAULT_TIMEOUT_MS;
	async function fetchJson(url: string, headers: Record<string, string>): Promise<unknown> {
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), timeoutMs);
		try {
			const response = await fetchImpl(url, { headers, signal: controller.signal });
			if (!response.ok) throw new Error(`peer answered ${response.status}`);
			return response.json();
		} finally { clearTimeout(timer); }
	}
	async function fetchProjects(baseUrl: string, headers: Record<string, string>): Promise<PeerProject[]> {
		const [projects, sessions] = await Promise.all([
			fetchJson(`${baseUrl}/api/v1/projects`, headers),
			fetchJson(`${baseUrl}/api/v1/sessions`, headers),
		]);
		return buildPeerProjects(projects, sessions);
	}
	async function localPeer(): Promise<PeerWorkspacesResult> {
		const contents = await deps.readLocalRunFile().catch(() => null);
		const info = contents ? parseRunFile(contents) : null;
		if (!info) return unavailable("This computer's local AO daemon is not running.");
		try {
			return { state: "ok", machineId: LOCAL_MACHINE_ID, machineName: deps.localMachineName, isRemote: false,
				projects: await fetchProjects(`http://127.0.0.1:${info.port}`, { Accept: "application/json" }) };
		} catch { return unavailable("This computer's local AO daemon is not reachable."); }
	}
	async function pairedPeer(machine: AoMachine): Promise<PeerWorkspacesResult> {
		const passcode = await deps.getPasscode(machine.id).catch(() => null);
		if (!passcode) return unavailable(`${machine.name} has no stored passcode. Re-pair it.`);
		try {
			return { state: "ok", machineId: machine.id, machineName: machine.name, isRemote: true,
				projects: await fetchProjects(machine.baseUrl, { Authorization: `Bearer ${passcode}`, Accept: "application/json" }) };
		} catch { return unavailable(`${machine.name} is not reachable.`); }
	}
	return { get: async () => {
		const state = deps.getMachinesState();
		if (state.activeMachineId !== LOCAL_MACHINE_ID) return localPeer();
		const peer = state.machines.find((machine) => !machine.local);
		return peer ? pairedPeer(peer) : unavailable("No paired machine is available.");
	} };
}
