import { describe, expect, it } from "vitest";
import { isRemoteDaemonBaseUrl, readRemoteDaemonConfig } from "./remote-daemon";

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
