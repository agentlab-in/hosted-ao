// @vitest-environment node
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { LOCAL_MACHINE_ID, type AoMachine } from "../shared/ao-machines";
import { AO_MACHINE_FILE_NAME, createAoMachinesController } from "./ao-machines";
import type { ControlPlaneTokenSource } from "./ao-control-token";
import type { SafeStorageLike } from "./ao-account-store";

const safeStorage: SafeStorageLike = {
	isEncryptionAvailable: () => true,
	encryptString: (plain) => Buffer.from(plain, "utf8"),
	decryptString: (cipher) => cipher.toString("utf8"),
};

const signedIn: ControlPlaneTokenSource = { get: async () => "cp_access_token", clear: () => undefined };
const signedOut: ControlPlaneTokenSource = { get: async () => null, clear: () => undefined };

const machineRow = (extra: Record<string, unknown> = {}) => ({
	id: "mch_1",
	name: "ao-build-01",
	public_url: "https://vm.example.com",
	created_at: "2026-07-01T10:00:00Z",
	last_seen: "2026-07-30T09:00:00Z",
	...extra,
});

const listResponse = (...rows: Array<Record<string, unknown>>) =>
	new Response(JSON.stringify({ machines: rows }), { status: 200, headers: { "Content-Type": "application/json" } });

/**
 * A fetch that answers the control plane's list route from `rows`, and answers
 * a machine's own origin according to `up`. Anything not in `up` throws, which
 * is what an unreachable machine looks like to fetch.
 */
function fakeFetch(rows: Array<Record<string, unknown>>, up: Record<string, boolean> = {}) {
	return vi.fn(async (input: string, init?: RequestInit) => {
		const url = String(input);
		if (url.endsWith("/api/v1/machines")) {
			expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer cp_access_token");
			return listResponse(...rows);
		}
		const origin = new URL(url).origin;
		if (up[origin]) return new Response("not found", { status: 404 });
		throw new Error("connect ECONNREFUSED");
	});
}

let stateDir = "";
let active: Array<AoMachine | null> = [];

beforeEach(async () => {
	stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-machines-"));
	active = [];
});
afterEach(async () => {
	await rm(stateDir, { recursive: true, force: true });
});

function controller(fetchImpl: ReturnType<typeof fakeFetch>, tokenSource: ControlPlaneTokenSource = signedIn) {
	return createAoMachinesController({
		stateDir,
		env: {},
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: fetchImpl as unknown as typeof fetch,
		probeTimeoutMs: 50,
		tokenSource,
	});
}

test("this computer is machine zero, and it is there before anything is listed", () => {
	const state = controller(fakeFetch([])).getState();
	expect(state.machines[0]).toMatchObject({ id: LOCAL_MACHINE_ID, name: "This Mac", local: true });
	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
});

test("signed out still offers this computer: local use never requires an account", async () => {
	const fetchImpl = fakeFetch([machineRow()]);
	const state = await controller(fetchImpl, signedOut).refresh();

	expect(state.status).toBe("signed-out");
	expect(state.machines.map((machine) => machine.id)).toEqual([LOCAL_MACHINE_ID]);
	expect(fetchImpl).not.toHaveBeenCalled();
});

test("lists the account's machines with a control-plane-audience bearer token", async () => {
	const fetchImpl = fakeFetch([machineRow()], { "https://vm.example.com": true });
	const state = await controller(fetchImpl).refresh();

	expect(state.status).toBe("ready");
	expect(state.machines.map((machine) => machine.id)).toEqual([LOCAL_MACHINE_ID, "mch_1"]);
	expect(state.machines[1]).toMatchObject({ reachability: "online", baseUrl: "https://vm.example.com" });
	// The Authorization header is asserted inside fakeFetch; this pins the route.
	expect(String(fetchImpl.mock.calls[0][0])).toBe("https://ao.agentlab.in/api/v1/machines");
});

test("a machine that does not answer is offline, with the last-seen the control plane reported", async () => {
	const state = await controller(fakeFetch([machineRow()])).refresh();
	expect(state.machines[1]).toMatchObject({ reachability: "offline", lastSeen: "2026-07-30T09:00:00Z" });
});

test("the liveness probe sends no credential of any kind", async () => {
	const fetchImpl = fakeFetch([machineRow()], { "https://vm.example.com": true });
	await controller(fetchImpl).refresh();

	const probe = fetchImpl.mock.calls.find(([url]) => String(url).startsWith("https://vm.example.com"));
	expect(probe).toBeDefined();
	const headers = new Headers((probe?.[1] as RequestInit | undefined)?.headers);
	expect(headers.get("Authorization")).toBeNull();
	expect(headers.get("Cookie")).toBeNull();
});

test("selecting a machine re-points the app and survives a restart", async () => {
	const fetchImpl = fakeFetch([machineRow()], { "https://vm.example.com": true });
	const machines = controller(fetchImpl);
	await machines.refresh();

	const state = await machines.select("mch_1");
	expect(state.activeMachineId).toBe("mch_1");
	expect(active.at(-1)).toMatchObject({ id: "mch_1", baseUrl: "https://vm.example.com" });

	// The whole record is on disk, not just the id, so the next launch knows it
	// is pointed at a remote machine before it can reach the control plane.
	const onDisk = JSON.parse(await readFile(path.join(stateDir, AO_MACHINE_FILE_NAME), "utf8"));
	expect(onDisk.machine).toMatchObject({ id: "mch_1", baseUrl: "https://vm.example.com" });

	active = [];
	const relaunched = controller(fakeFetch([machineRow()]));
	await expect(relaunched.restore()).resolves.toMatchObject({ id: "mch_1", baseUrl: "https://vm.example.com" });
	expect(active).toHaveLength(1);
});

test("switching back to this computer leaves no machine on disk", async () => {
	const machines = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await machines.refresh();
	await machines.select("mch_1");

	const state = await machines.select(LOCAL_MACHINE_ID);
	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
	expect(active.at(-1)).toBeNull();
	await expect(readFile(path.join(stateDir, AO_MACHINE_FILE_NAME), "utf8")).rejects.toThrow();
});

test("an id that is not in the list is refused rather than guessed at", async () => {
	const machines = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await machines.refresh();

	const state = await machines.select("mch_someone_elses");
	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
	expect(state.error).toMatch(/no longer in this account's list/);
});

test("a revoked machine drops out of the list and the app falls back to this computer", async () => {
	const machines = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await machines.refresh();
	await machines.select("mch_1");

	const revoked = createAoMachinesController({
		stateDir,
		env: {},
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: fakeFetch([]) as unknown as typeof fetch,
		probeTimeoutMs: 50,
		tokenSource: signedIn,
	});
	await revoked.restore();
	const state = await revoked.refresh();

	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
	expect(state.machines.map((machine) => machine.id)).toEqual([LOCAL_MACHINE_ID]);
});

test("a control-plane outage keeps the active machine and still reports whether it is up", async () => {
	const machines = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await machines.refresh();
	await machines.select("mch_1");

	const offline = createAoMachinesController({
		stateDir,
		env: {},
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: (async (input: string) => {
			if (String(input).endsWith("/api/v1/machines")) throw new Error("getaddrinfo ENOTFOUND ao.agentlab.in");
			return new Response("not found", { status: 404 });
		}) as unknown as typeof fetch,
		probeTimeoutMs: 50,
		tokenSource: signedIn,
	});
	await offline.restore();
	const state = await offline.refresh();

	expect(state.status).toBe("error");
	expect(state.error).toMatch(/ENOTFOUND/);
	expect(state.activeMachineId).toBe("mch_1");
	expect(state.machines[1]).toMatchObject({ id: "mch_1", reachability: "online" });
});

test("signing out forgets the token and falls back to this computer", async () => {
	const clear = vi.fn();
	const machines = createAoMachinesController({
		stateDir,
		env: {},
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: fakeFetch([machineRow()], { "https://vm.example.com": true }) as unknown as typeof fetch,
		probeTimeoutMs: 50,
		tokenSource: { get: async () => "cp_access_token", clear },
	});
	await machines.refresh();
	await machines.select("mch_1");

	await machines.reset();

	expect(clear).toHaveBeenCalledTimes(1);
	expect(machines.getState()).toMatchObject({ status: "signed-out", activeMachineId: LOCAL_MACHINE_ID });
	expect(active.at(-1)).toBeNull();
});

test("a refresh already in flight cannot re-point a signed-out install", async () => {
	let releaseList = (): void => undefined;
	const listArrived = new Promise<void>((resolve) => {
		releaseList = resolve;
	});
	// A previous run left this computer pointed at mch_1.
	const before = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await before.refresh();
	await before.select("mch_1");

	const machines = createAoMachinesController({
		stateDir,
		env: {},
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: (async (input: string) => {
			if (String(input).endsWith("/api/v1/machines")) {
				await listArrived;
				return listResponse(machineRow());
			}
			return new Response("not found", { status: 404 });
		}) as unknown as typeof fetch,
		probeTimeoutMs: 50,
		tokenSource: signedIn,
	});
	await machines.restore();

	// Sign out lands while the list request is still open, holding a token the
	// refresh fetched before the reset.
	const inFlight = machines.refresh();
	await machines.reset();
	releaseList();
	await inFlight;

	expect(machines.getState()).toMatchObject({ status: "signed-out", activeMachineId: LOCAL_MACHINE_ID });
	expect(active.at(-1)).toBeNull();
	await expect(readFile(path.join(stateDir, AO_MACHINE_FILE_NAME), "utf8")).rejects.toThrow();
});

test("the machine file's temp name is random, so a stale one from a reused pid cannot be adopted", async () => {
	const machines = controller(fakeFetch([machineRow()], { "https://vm.example.com": true }));
	await machines.refresh();
	await machines.select("mch_1");
	await expect(readFile(path.join(stateDir, `.ao-machine-${process.pid}.json`), "utf8")).rejects.toThrow();
});

test("a bad AO_CONTROL_URL is reported, never a silent fall back to production", async () => {
	const machines = createAoMachinesController({
		stateDir,
		env: { AO_CONTROL_URL: "not a url" },
		safeStorage,
		localMachineName: "This Mac",
		onActiveChange: (machine) => active.push(machine),
		fetchImpl: fakeFetch([machineRow()]) as unknown as typeof fetch,
	});
	const state = await machines.refresh();
	expect(state.status).toBe("error");
	expect(state.error).toMatch(/AO_CONTROL_URL/);
});
