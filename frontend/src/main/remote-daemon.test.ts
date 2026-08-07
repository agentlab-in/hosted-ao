import { describe, expect, it, vi } from "vitest";
import type { AoMachine } from "../shared/ao-machines";
import { machineAuthFailedStatus, machineDaemonStatus } from "../shared/remote-daemon";
import { createRemoteDaemonLifecycle } from "./remote-daemon";

const machine = (extra: Partial<AoMachine> = {}): AoMachine => ({
	id: "mch_1",
	name: "ao-build-01",
	baseUrl: "https://vm.example.com",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
	...extra,
});

describe("an active registered machine", () => {
	const localCalls = () => {
		const spawnLocalDaemon = vi.fn();
		return {
			spawnLocalDaemon,
			localRefresh: vi.fn(async () => {
				spawnLocalDaemon();
				return { state: "stopped" as const };
			}),
			localStart: vi.fn(async () => {
				spawnLocalDaemon();
				return { state: "stopped" as const };
			}),
			localStop: vi.fn(() => ({ state: "stopped" as const })),
		};
	};

	it("re-points the app at that machine's base URL", async () => {
		const lifecycle = createRemoteDaemonLifecycle(() => machineDaemonStatus(machine()));
		const { localStart } = localCalls();

		await expect(lifecycle.start(localStart, vi.fn())).resolves.toEqual({
			state: "ready",
			baseUrl: "https://vm.example.com",
			message: "Connected to ao-build-01",
		});
		expect(localStart).not.toHaveBeenCalled();
	});

	// A credential failure is still a status the remote lifecycle owns, so it
	// cannot fall through to spawning a local daemon either.
	it("never spawns a local daemon when it has no credential", async () => {
		const lifecycle = createRemoteDaemonLifecycle(() =>
			machineAuthFailedStatus(machine(), "This computer is signed out of AO."),
		);
		const { localRefresh, localStart, localStop, spawnLocalDaemon } = localCalls();
		const setStatus = vi.fn();

		const unauthorized = { state: "error", code: "machine_auth_failed" };
		await expect(lifecycle.refresh(localRefresh, setStatus)).resolves.toMatchObject(unauthorized);
		await expect(lifecycle.start(localStart, setStatus)).resolves.toMatchObject(unauthorized);
		expect(lifecycle.stop(localStop, setStatus)).toMatchObject(unauthorized);

		expect(localRefresh).not.toHaveBeenCalled();
		expect(localStart).not.toHaveBeenCalled();
		expect(localStop).not.toHaveBeenCalled();
		expect(spawnLocalDaemon).not.toHaveBeenCalled();
	});

	// The hazard this guards: the app spawns a local daemon when it cannot reach
	// one, and doing that because a remote machine is down would be wrong. The
	// local functions are arguments the lifecycle simply never calls while a
	// machine is active, so being down cannot reach them by any path.
	it("never spawns a local daemon when it is unreachable", async () => {
		const down = machine({ reachability: "offline", lastSeen: "2026-07-30T09:00:00Z" });
		const lifecycle = createRemoteDaemonLifecycle(() => machineDaemonStatus(down));
		const { localRefresh, localStart, localStop, spawnLocalDaemon } = localCalls();
		const setStatus = vi.fn();

		const offline = {
			state: "error",
			code: "daemon_unreachable",
			message: expect.stringContaining("ao-build-01 is not reachable. Last seen"),
		};
		await expect(lifecycle.refresh(localRefresh, setStatus)).resolves.toMatchObject(offline);
		await expect(lifecycle.start(localStart, setStatus)).resolves.toMatchObject(offline);
		expect(lifecycle.stop(localStop, setStatus)).toMatchObject(offline);

		expect(localRefresh).not.toHaveBeenCalled();
		expect(localStart).not.toHaveBeenCalled();
		expect(localStop).not.toHaveBeenCalled();
		expect(spawnLocalDaemon).not.toHaveBeenCalled();
	});

	it("hands back to the local daemon only when this computer is the active machine", async () => {
		const lifecycle = createRemoteDaemonLifecycle(() => null);
		const { localStart } = localCalls();

		await expect(lifecycle.start(localStart, vi.fn())).resolves.toEqual({ state: "stopped" });
		expect(localStart).toHaveBeenCalledTimes(1);
	});
});
