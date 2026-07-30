import { describe, expect, it } from "vitest";
import type { AoMachine } from "./ao-machines";
import { isRemoteDaemonBaseUrl, machineDaemonStatus, readRemoteDaemonConfig } from "./remote-daemon";

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

	// The regression this guards: a "ready" status here sets the renderer's API
	// base URL, and with no machine-audience token in this build every REST call
	// and both EventSources would 401 under a UI that says Connected.
	it("does not hand out a base URL for a reachable machine while there is no credential for it", () => {
		const status = machineDaemonStatus(machine({ reachability: "online" }));
		expect(status.state).not.toBe("ready");
		expect(status.baseUrl).toBeUndefined();
		expect(status.message).toContain("ao-build-01");
	});
});
