import { describe, expect, it } from "vitest";
import type { AoMachine } from "./ao-machines";
import {
	GATEWAY_COOKIE_NAME,
	isRemoteDaemonBaseUrl,
	machineAuthFailedStatus,
	machineDaemonStatus,
	readRemoteDaemonConfig,
	REMOTE_PAIRING_COOKIE_NAME,
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

describe("readRemoteDaemonConfig", () => {
	it("returns a normalized remote config for an HTTPS origin and URL-safe token", () => {
		expect(readRemoteDaemonConfig({
			AO_REMOTE_URL: "https://api.ao.agentlab.in/",
			AO_REMOTE_TOKEN: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		})).toEqual({
			baseUrl: "https://api.ao.agentlab.in",
			token: "dGVzdF9wYWlyaW5nLXNlY3JldA",
		});
	});

	it("rejects a partial remote configuration without returning a local fallback", () => {
		expect(() => readRemoteDaemonConfig({ AO_REMOTE_URL: "https://api.ao.agentlab.in" }))
			.toThrow("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	});

	it("returns null when neither remote variable is set", () => {
		expect(readRemoteDaemonConfig({})).toBeNull();
	});

	it("rejects token-only input", () => {
		expect(() => readRemoteDaemonConfig({ AO_REMOTE_TOKEN: "token" }))
			.toThrow("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	});

	it.each([
		["http://api.ao.agentlab.in", "HTTP URLs"],
		["https://api.ao.agentlab.in/path", "URL paths"],
		["https://api.ao.agentlab.in?query=1", "query strings"],
		["https://api.ao.agentlab.in?", "bare query delimiters"],
		["https://api.ao.agentlab.in#fragment", "fragments"],
		["https://api.ao.agentlab.in#", "bare fragment delimiters"],
		["https://user:pass@api.ao.agentlab.in", "credentials"],
	])("rejects %s (%s)", (url) => {
		expect(() => readRemoteDaemonConfig({ AO_REMOTE_URL: url, AO_REMOTE_TOKEN: "token" }))
			.toThrow("AO_REMOTE_URL must be an HTTPS origin without a path, query, fragment, or credentials");
	});

	it.each([["token with space"], ["token/with/slash"]])("rejects URL-unsafe token %s", (token) => {
		expect(() => readRemoteDaemonConfig({ AO_REMOTE_URL: "https://api.ao.agentlab.in", AO_REMOTE_TOKEN: token }))
			.toThrow("AO_REMOTE_TOKEN must be URL-safe base64");
	});
});

describe("isRemoteDaemonBaseUrl", () => {
	it("recognizes HTTPS remote origins", () => {
		expect(isRemoteDaemonBaseUrl("https://api.ao.agentlab.in")).toBe(true);
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
	it("is the name the VM gateway reads, and is not the AO_REMOTE_URL pairing cookie", () => {
		expect(GATEWAY_COOKIE_NAME).toBe("ao_gw_token");
		expect(GATEWAY_COOKIE_NAME).not.toBe(REMOTE_PAIRING_COOKIE_NAME);
	});
});
