import { describe, expect, it } from "vitest";
import type { AoMachine } from "./ao-machines";
import {
	GATEWAY_COOKIE_NAME,
	isRemoteDaemonBaseUrl,
	machineAuthFailedStatus,
	machineDaemonStatus,
} from "./remote-daemon";

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

describe("isRemoteDaemonBaseUrl", () => {
	it("recognizes HTTPS remote origins", () => {
		expect(isRemoteDaemonBaseUrl("https://vm.example.com")).toBe(true);
	});

	it("does not recognize loopback HTTP", () => {
		expect(isRemoteDaemonBaseUrl("http://127.0.0.1:4317")).toBe(false);
	});
});

describe("machineDaemonStatus", () => {
	it("says when a machine was last seen, or that it never has been", () => {
		expect(machineDaemonStatus(machine({ reachability: "offline" })).message).toContain("never connected");
		expect(machineDaemonStatus(machine({ reachability: "offline", lastSeen: "2020-01-01T09:00:00Z" })).message)
			.toMatch(/Last seen .+ ago\./);
	});

	it("is connecting, not ready, before the machine has answered", () => {
		expect(machineDaemonStatus(machine({ reachability: "unknown" }))).toMatchObject({ state: "starting" });
		// Nothing is pointed at a machine that has not answered yet.
		expect(machineDaemonStatus(machine({ reachability: "unknown" })).baseUrl).toBeUndefined();
	});

	it("hands the reachable machine's base URL to the renderer", () => {
		expect(machineDaemonStatus(machine({ reachability: "online" }))).toEqual({
			state: "ready",
			baseUrl: "https://vm.example.com",
			message: "Connected to ao-build-01",
		});
	});

	// The regression this guards: a "ready" status sets the renderer's API base
	// URL, so a machine that has not answered, or is down, must not carry one.
	// Only main/machine-transport.ts publishes the ready case, and only once a
	// machine-audience token is held and the ao_gw_token cookie is installed.
	it.each(["offline", "unknown"] as const)("does not hand out a base URL while reachability is %s", (reachability) => {
		expect(machineDaemonStatus(machine({ reachability })).baseUrl).toBeUndefined();
	});
});

describe("machineAuthFailedStatus", () => {
	it("names the machine and the reason without handing out a base URL", () => {
		expect(machineAuthFailedStatus(machine(), "This computer is signed out of AO.")).toEqual({
			state: "error",
			code: "machine_auth_failed",
			message: "Could not sign in to ao-build-01. This computer is signed out of AO.",
		});
	});
});

describe("GATEWAY_COOKIE_NAME", () => {
	// The gateway reads this exact name (gatewayCookieName in
	// backend/internal/vmgateway/proxy.go). A rename on either side makes /mux and
	// the SSE stream 401 with nothing to see on the client, so it is pinned here.
	it("is the name the VM gateway reads", () => {
		expect(GATEWAY_COOKIE_NAME).toBe("ao_gw_token");
	});
});
