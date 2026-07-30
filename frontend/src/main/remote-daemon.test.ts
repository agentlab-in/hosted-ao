import { describe, expect, it, vi } from "vitest";
import type { AoMachine } from "../shared/ao-machines";
import { machineAuthFailedStatus, machineDaemonStatus } from "../shared/remote-daemon";
import { createRemoteDaemonLifecycle, installRemoteDaemonCookie, remoteDaemonReadyStatus } from "./remote-daemon";

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

describe("installRemoteDaemonCookie", () => {
	it("installs the pairing secret as a secure HttpOnly remote-origin cookie", async () => {
		const set = vi.fn().mockResolvedValue(undefined);

		await installRemoteDaemonCookie({ set }, {
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		});

		expect(set).toHaveBeenCalledWith(expect.objectContaining({
			url: "https://api.ao.agentlab.in",
			name: "ao_hosted_pair",
			value: "dGVzdF9wYWlyaW5nLXNlY3JldA",
			httpOnly: true,
			secure: true,
			sameSite: "no_restriction",
			path: "/",
		}));
		expect(set.mock.calls[0]?.[0]).not.toHaveProperty("domain");
	});

	it("propagates cookie store failures", async () => {
		const error = new Error("cookie store unavailable");
		const set = vi.fn().mockRejectedValue(error);

		await expect(installRemoteDaemonCookie({ set }, {
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		})).rejects.toThrow(error);
	});
});

describe("createRemoteDaemonLifecycle", () => {
	it("returns remote-ready statuses without entering local lifecycle dependencies", async () => {
		const resolveDaemonLaunch = vi.fn();
		const inspectExistingDaemon = vi.fn();
		const spawnDaemon = vi.fn();
		const establishSupervisorLink = vi.fn();
		const setStatus = vi.fn();
		const lifecycle = createRemoteDaemonLifecycle({
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		});

		const localRefresh = vi.fn(async () => {
			resolveDaemonLaunch();
			inspectExistingDaemon();
			return { state: "stopped" as const };
		});
		const localStart = vi.fn(async () => {
			resolveDaemonLaunch();
			inspectExistingDaemon();
			spawnDaemon();
			establishSupervisorLink();
			return { state: "stopped" as const };
		});
		const localStop = vi.fn(() => {
			establishSupervisorLink();
			return { state: "stopped" as const };
		});

		const expected = { state: "ready" as const, baseUrl: "https://api.ao.agentlab.in", message: "Connected to remote daemon" };
		await expect(lifecycle.refresh(localRefresh, setStatus)).resolves.toEqual(expected);
		await expect(lifecycle.start(localStart, setStatus)).resolves.toEqual(expected);
		expect(lifecycle.stop(localStop, setStatus)).toEqual(expected);
		expect(localRefresh).not.toHaveBeenCalled();
		expect(localStart).not.toHaveBeenCalled();
		expect(localStop).not.toHaveBeenCalled();
		expect(resolveDaemonLaunch).not.toHaveBeenCalled();
		expect(inspectExistingDaemon).not.toHaveBeenCalled();
		expect(spawnDaemon).not.toHaveBeenCalled();
		expect(establishSupervisorLink).not.toHaveBeenCalled();
	});

	it("keeps local lifecycle dependencies untouched when cookie installation fails", async () => {
		const setStatus = vi.fn();
		const localRefresh = vi.fn(async () => ({ state: "stopped" as const }));
		const localStart = vi.fn(async () => ({ state: "stopped" as const }));
		const localStop = vi.fn(() => ({ state: "stopped" as const }));
		const lifecycle = createRemoteDaemonLifecycle({
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		});

		await expect(lifecycle.installCookie({
			set: vi.fn().mockRejectedValue(new Error("cookie store unavailable")),
		})).resolves.toEqual({
			state: "error",
			message: "Could not configure remote daemon authentication.",
			code: "not_configured",
		});
		await expect(lifecycle.refresh(localRefresh, setStatus)).resolves.toEqual({
			state: "error",
			message: "Could not configure remote daemon authentication.",
			code: "not_configured",
		});
		await expect(lifecycle.start(localStart, setStatus)).resolves.toEqual({
			state: "error",
			message: "Could not configure remote daemon authentication.",
			code: "not_configured",
		});
		expect(lifecycle.stop(localStop, setStatus)).toEqual({
			state: "error",
			message: "Could not configure remote daemon authentication.",
			code: "not_configured",
		});
		expect(localRefresh).not.toHaveBeenCalled();
		expect(localStart).not.toHaveBeenCalled();
		expect(localStop).not.toHaveBeenCalled();
	});
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
		const lifecycle = createRemoteDaemonLifecycle(null, null, () => machineDaemonStatus(machine()));
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
		const lifecycle = createRemoteDaemonLifecycle(null, null, () =>
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
		const lifecycle = createRemoteDaemonLifecycle(null, null, () => machineDaemonStatus(down));
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
		const lifecycle = createRemoteDaemonLifecycle(null, null, () => null);
		const { localStart } = localCalls();

		await expect(lifecycle.start(localStart, vi.fn())).resolves.toEqual({ state: "stopped" });
		expect(localStart).toHaveBeenCalledTimes(1);
	});

	it("does not displace AO_REMOTE_URL, which keeps behaving exactly as before", async () => {
		const config = { baseUrl: "https://api.ao.agentlab.in", token: "dGVzdF9wYWlyaW5nLXNlY3JldA" };
		const lifecycle = createRemoteDaemonLifecycle(config, null, () => machineDaemonStatus(machine()));

		await expect(lifecycle.start(vi.fn(), vi.fn())).resolves.toEqual(remoteDaemonReadyStatus(config));
	});
});

describe("remoteDaemonReadyStatus", () => {
	it("returns a ready status without exposing the pairing token", () => {
		expect(remoteDaemonReadyStatus({ baseUrl: "https://api.ao.agentlab.in", token: "secret" }))
			.toEqual({ state: "ready", baseUrl: "https://api.ao.agentlab.in", message: "Connected to remote daemon" });
	});

	it("omits the token field even when the supplied config has one", () => {
		expect(remoteDaemonReadyStatus({ baseUrl: "https://api.ao.agentlab.in", token: "secret" }))
			.not.toHaveProperty("token");
	});
});
