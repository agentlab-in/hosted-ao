import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LOCAL_MACHINE_ID, type AoMachine, type AoMachinesState } from "../shared/ao-machines";
import type { ControlPlaneTokenSource } from "./ao-control-token";
import type { MachineAccessToken, MachineTokenSource } from "./ao-machine-token";
import { MachineUnavailableError } from "./ao-machine-token";
import type { AoControlPlaneCredential } from "./ao-machines";
import { SIGNED_OUT_REASON } from "./machine-transport";
import { buildPeerProjects, createPeerWorkspacesController, type PeerWorkspacesDeps } from "./peer-workspaces";

const REMOTE_MACHINE: AoMachine = {
	id: "mch_1",
	name: "ao-build-01",
	baseUrl: "https://vm.example.com",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
};

const localState = (extra: Partial<AoMachinesState> = {}): AoMachinesState => ({
	status: "ready",
	machines: [
		{
			id: LOCAL_MACHINE_ID,
			name: "This Mac",
			baseUrl: "",
			local: true,
			createdAt: null,
			lastSeen: null,
			reachability: "online",
			harness: "unknown",
			harnessCommand: null,
		},
	],
	activeMachineId: LOCAL_MACHINE_ID,
	...extra,
});

const remoteState = (): AoMachinesState => ({
	...localState(),
	machines: [...localState().machines, REMOTE_MACHINE],
	activeMachineId: REMOTE_MACHINE.id,
});

const projectsResponse = (...projects: Array<Record<string, unknown>>) =>
	new Response(JSON.stringify({ projects }), { status: 200, headers: { "Content-Type": "application/json" } });

const sessionsResponse = (...sessions: Array<Record<string, unknown>>) =>
	new Response(JSON.stringify({ sessions }), { status: 200, headers: { "Content-Type": "application/json" } });

const project = (extra: Record<string, unknown> = {}) => ({ id: "proj_1", name: "widgets", ...extra });
const session = (extra: Record<string, unknown> = {}) => ({
	id: "sess_1",
	projectId: "proj_1",
	displayName: "Fix the thing",
	status: "working",
	activity: { state: "active", lastActivityAt: "2026-08-01T00:00:00Z" },
	branch: "sess/1",
	harness: "claude-code",
	kind: "worker",
	updatedAt: "2026-08-01T00:00:00Z",
	...extra,
});

/** A fetch that answers /api/v1/projects and /api/v1/sessions on any origin. */
function fakeFetch(projects: Array<Record<string, unknown>>, sessions: Array<Record<string, unknown>>) {
	return vi.fn(async (input: string) => {
		const url = String(input);
		if (url.endsWith("/api/v1/projects")) return projectsResponse(...projects);
		if (url.endsWith("/api/v1/sessions")) return sessionsResponse(...sessions);
		throw new Error(`unexpected url: ${url}`);
	});
}

const MACHINE_TOKEN = "machine.audience.jwt";

/** A MachineTokenSource under the test's control, mirroring machine-transport.test.ts's fakeTokens. */
function fakeTokenSource() {
	const state = { mode: "ok" as "ok" | "signed-out" | "gone" | "error", created: [] as string[] };
	const token: MachineAccessToken = { token: MACHINE_TOKEN, expiresAt: Date.now() + 900_000 };
	const source: MachineTokenSource = {
		get: async () => {
			if (state.mode === "signed-out") return null;
			if (state.mode === "gone") throw new MachineUnavailableError();
			if (state.mode === "error") throw new Error("The control plane is unreachable.");
			return token;
		},
		mint: async () => token,
		clear: () => undefined,
	};
	return { state, source };
}

function credential(): AoControlPlaneCredential {
	const controlToken: ControlPlaneTokenSource = { get: async () => "control.plane.jwt", clear: () => undefined };
	return { controlPlaneUrl: "https://ao.agentlab.in", token: controlToken };
}

function harness(overrides: Partial<PeerWorkspacesDeps> = {}) {
	const tokens = fakeTokenSource();
	const createTokenSource = vi.fn((deps: { machineId: string }) => {
		tokens.state.created.push(deps.machineId);
		return tokens.source;
	});
	const readLocalRunFile = vi.fn(async () => null as string | null);
	const deps: PeerWorkspacesDeps = {
		getMachinesState: () => localState(),
		credential: () => credential(),
		localMachineName: "This Mac",
		readLocalRunFile,
		fetchImpl: fakeFetch([project()], [session()]) as unknown as typeof fetch,
		createTokenSource,
		...overrides,
	};
	return { deps, tokens, readLocalRunFile, controller: createPeerWorkspacesController(deps) };
}

describe("buildPeerProjects", () => {
	it("shapes projects and sessions to the peer contract, grouping sessions by project", () => {
		const projects = buildPeerProjects({ projects: [project()] }, { sessions: [session()] });

		expect(projects).toEqual([
			{
				id: "proj_1",
				name: "widgets",
				sessions: [
					{
						id: "sess_1",
						title: "Fix the thing",
						status: "working",
						activity: "active",
						branch: "sess/1",
						harness: "claude-code",
						kind: "worker",
						updatedAt: "2026-08-01T00:00:00Z",
					},
				],
			},
		]);
	});

	it("falls back to the id as title when displayName is blank", () => {
		const projects = buildPeerProjects({ projects: [project()] }, { sessions: [session({ displayName: "" })] });
		expect(projects[0]?.sessions[0]?.title).toBe("sess_1");
	});

	it("drops unusable rows instead of failing the whole list", () => {
		const projects = buildPeerProjects(
			{ projects: [project(), { name: "no id" }] },
			{ sessions: [session(), { id: "orphan" }, "garbage"] },
		);
		expect(projects).toHaveLength(1);
		expect(projects[0]?.sessions).toHaveLength(1);
	});

	it("gives a session a home even when its project id is missing from the projects list", () => {
		const projects = buildPeerProjects({ projects: [] }, { sessions: [session({ projectId: "proj_orphan" })] });
		expect(projects).toEqual([expect.objectContaining({ id: "proj_orphan", name: "proj_orphan" })]);
	});

	it("tolerates a malformed body instead of throwing", () => {
		expect(buildPeerProjects(null, undefined)).toEqual([]);
		expect(buildPeerProjects("garbage", { sessions: "garbage" })).toEqual([]);
	});
});

const REMOTE_ACTIVE_STATE: AoMachinesState = {
	...localState(),
	machines: [...localState().machines, REMOTE_MACHINE],
	// The registered machine is in the list but NOT active: this computer is
	// active, which is what makes the registered machine the peer.
	activeMachineId: LOCAL_MACHINE_ID,
};

describe("remote peer (this computer is active)", () => {
	it("fetches the registered machine's projects and sessions with a Bearer token and no cookie", async () => {
		const { controller, deps } = harness({ getMachinesState: () => REMOTE_ACTIVE_STATE });

		const result = await controller.get();

		expect(result).toEqual({
			state: "ok",
			machineId: REMOTE_MACHINE.id,
			machineName: REMOTE_MACHINE.name,
			isRemote: true,
			projects: [
				{
					id: "proj_1",
					name: "widgets",
					sessions: [expect.objectContaining({ id: "sess_1", title: "Fix the thing" })],
				},
			],
		});
		const fetchMock = deps.fetchImpl as ReturnType<typeof vi.fn>;
		expect(fetchMock.mock.calls[0][1]).toMatchObject({
			headers: { Authorization: `Bearer ${MACHINE_TOKEN}`, Accept: "application/json" },
		});
		// No cookie is ever installed for a peer: it never opens /mux or SSE.
		for (const [, init] of fetchMock.mock.calls) {
			expect(JSON.stringify(init)).not.toMatch(/cookie/i);
		}
	});

	it("mints from a second, independent token source, not machine-transport's", async () => {
		const { tokens, controller } = harness({ getMachinesState: () => REMOTE_ACTIVE_STATE });

		await controller.get();

		expect(tokens.state.created).toEqual([REMOTE_MACHINE.id]);
	});

	it("returns unavailable when no registered machine is online", async () => {
		const { controller } = harness({ getMachinesState: () => localState() });

		await expect(controller.get()).resolves.toEqual({
			state: "unavailable",
			reason: "No other machine is online for this account.",
		});
	});

	it("returns unavailable when signed out, without calling fetch", async () => {
		const { deps, controller } = harness({ getMachinesState: () => REMOTE_ACTIVE_STATE, credential: () => null });

		await expect(controller.get()).resolves.toEqual({ state: "unavailable", reason: SIGNED_OUT_REASON });
		expect(deps.fetchImpl).not.toHaveBeenCalled();
	});

	it("returns unavailable, not a throw, when the peer is offline", async () => {
		const failingFetch = vi.fn(async () => {
			throw new Error("connect ECONNREFUSED");
		});
		const { controller } = harness({
			getMachinesState: () => REMOTE_ACTIVE_STATE,
			fetchImpl: failingFetch as unknown as typeof fetch,
		});

		await expect(controller.get()).resolves.toEqual({
			state: "unavailable",
			reason: `${REMOTE_MACHINE.name} is not reachable.`,
		});
	});
});

describe("local peer (a registered machine is active)", () => {
	it("reads the loopback daemon's port from running.json and fetches it unauthenticated", async () => {
		const readLocalRunFile = vi.fn(async () => JSON.stringify({ pid: 123, port: 4123 }));
		const { controller, deps } = harness({
			getMachinesState: () => remoteState(),
			readLocalRunFile,
		});

		const result = await controller.get();

		expect(result).toEqual({
			state: "ok",
			machineId: LOCAL_MACHINE_ID,
			machineName: "This Mac",
			isRemote: false,
			projects: [
				{
					id: "proj_1",
					name: "widgets",
					sessions: [expect.objectContaining({ id: "sess_1" })],
				},
			],
		});
		const [url, init] = (deps.fetchImpl as ReturnType<typeof vi.fn>).mock.calls[0];
		expect(url).toBe("http://127.0.0.1:4123/api/v1/projects");
		expect(init).not.toHaveProperty("headers.Authorization");
	});

	it("never hardcodes 3001: the port comes from running.json", async () => {
		const readLocalRunFile = vi.fn(async () => JSON.stringify({ pid: 1, port: 9999 }));
		const { controller, deps } = harness({ getMachinesState: () => remoteState(), readLocalRunFile });

		await controller.get();

		const urls = (deps.fetchImpl as ReturnType<typeof vi.fn>).mock.calls.map((call) => call[0]);
		expect(urls).toEqual(["http://127.0.0.1:9999/api/v1/projects", "http://127.0.0.1:9999/api/v1/sessions"]);
	});

	it("returns unavailable when no local daemon is running", async () => {
		const { controller } = harness({ getMachinesState: () => remoteState(), readLocalRunFile: async () => null });

		await expect(controller.get()).resolves.toEqual({
			state: "unavailable",
			reason: "This computer's local AO daemon is not running.",
		});
	});

	it("returns unavailable when running.json cannot be parsed", async () => {
		const { controller } = harness({
			getMachinesState: () => remoteState(),
			readLocalRunFile: async () => "not json",
		});

		await expect(controller.get()).resolves.toEqual({
			state: "unavailable",
			reason: "This computer's local AO daemon is not running.",
		});
	});

	it("returns unavailable when readLocalRunFile rejects", async () => {
		const { controller } = harness({
			getMachinesState: () => remoteState(),
			readLocalRunFile: async () => {
				throw new Error("EPERM");
			},
		});

		await expect(controller.get()).resolves.toEqual({
			state: "unavailable",
			reason: "This computer's local AO daemon is not running.",
		});
	});
});

describe("timeouts", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it("aborts a peer fetch that never answers, rather than hanging the UI", async () => {
		const hangingFetch = vi.fn(
			(_url: string, init?: RequestInit) =>
				new Promise((_resolve, reject) => {
					init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
				}),
		);
		const { controller } = harness({
			getMachinesState: () => remoteState(),
			readLocalRunFile: async () => JSON.stringify({ pid: 1, port: 4123 }),
			fetchImpl: hangingFetch as unknown as typeof fetch,
			timeoutMs: 2_000,
		});

		const pending = controller.get();
		await vi.advanceTimersByTimeAsync(2_000);

		await expect(pending).resolves.toEqual({
			state: "unavailable",
			reason: "This computer's local AO daemon is not reachable.",
		});
	});
});

describe("token safety", () => {
	it("never puts the token value in a returned reason", async () => {
		const failingFetch = vi.fn(async () => {
			throw new Error("connect ECONNREFUSED");
		});
		const { controller } = harness({
			getMachinesState: () => REMOTE_ACTIVE_STATE,
			fetchImpl: failingFetch as unknown as typeof fetch,
		});

		const result = await controller.get();

		expect(JSON.stringify(result)).not.toContain(MACHINE_TOKEN);
		expect(JSON.stringify(result)).not.toContain("control.plane.jwt");
	});

	it("never puts the token value in an unavailable reason for a local-peer timeout either", async () => {
		vi.useFakeTimers();
		try {
			const hangingFetch = vi.fn(
				(_url: string, init?: RequestInit) =>
					new Promise((_resolve, reject) => {
						init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
					}),
			);
			const { controller } = harness({
				getMachinesState: () => remoteState(),
				readLocalRunFile: async () => JSON.stringify({ pid: 1, port: 4123 }),
				fetchImpl: hangingFetch as unknown as typeof fetch,
				timeoutMs: 2_000,
			});

			const pending = controller.get();
			await vi.advanceTimersByTimeAsync(2_000);
			const result = await pending;

			expect(JSON.stringify(result)).not.toContain(MACHINE_TOKEN);
		} finally {
			vi.useRealTimers();
		}
	});
});
