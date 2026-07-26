import { describe, expect, it, vi } from "vitest";
import { installRemoteDaemonCookie, remoteDaemonReadyStatus } from "./remote-daemon";

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
			httpOnly: true,
			secure: true,
			sameSite: "no_restriction",
			path: "/",
		}));
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
