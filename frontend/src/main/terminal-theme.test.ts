import { describe, expect, it, vi } from "vitest";

import { persistSelectedDaemonTerminalTheme } from "./terminal-theme";

describe("persistSelectedDaemonTerminalTheme", () => {
	it("writes only the local daemon data directory when local is selected", async () => {
		const writeLocal = vi.fn();
		const fetchRemote = vi.fn();
		await persistSelectedDaemonTerminalTheme("dark", {
			isRemoteSelected: () => false,
			activeRemoteStatus: () => null,
			gatewayToken: async () => null,
			writeLocal,
			fetchRemote,
		});
		expect(writeLocal).toHaveBeenCalledWith("dark");
		expect(fetchRemote).not.toHaveBeenCalled();
	});

	it("sends the hint to the selected daemon through its authenticated gateway", async () => {
		const writeLocal = vi.fn();
		const fetchRemote = vi.fn(async () => ({ ok: true, status: 204 }));
		await persistSelectedDaemonTerminalTheme("light", {
			isRemoteSelected: () => true,
			activeRemoteStatus: () => ({ state: "ready", baseUrl: "https://box.example" }),
			gatewayToken: async () => "pair-passcode",
			writeLocal,
			fetchRemote,
		});
		expect(writeLocal).not.toHaveBeenCalled();
		expect(fetchRemote).toHaveBeenCalledWith(
			"https://box.example/api/v1/settings/terminal-theme",
			expect.objectContaining({
				method: "PATCH",
				headers: expect.objectContaining({ Authorization: "Bearer pair-passcode" }),
				body: JSON.stringify({ scheme: "light" }),
			}),
		);
	});

	it("never writes local state as a fallback for an unavailable remote", async () => {
		const writeLocal = vi.fn();
		await expect(persistSelectedDaemonTerminalTheme("dark", {
			isRemoteSelected: () => true,
			activeRemoteStatus: () => ({ state: "error" }),
			gatewayToken: async () => null,
			writeLocal,
			fetchRemote: vi.fn(),
		})).rejects.toThrow("not ready");
		expect(writeLocal).not.toHaveBeenCalled();
	});
});
