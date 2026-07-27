import { describe, expect, it, vi } from "vitest";
import { createRemoteDaemonLifecycle, installRemoteDaemonCookie, remoteDaemonReadyStatus } from "./remote-daemon";

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
