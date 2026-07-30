import { expect, test } from "vitest";
import { DEFAULT_CONTROL_PLANE_URL, readControlPlaneUrl } from "./control-plane";

test("defaults to the hosted control plane when AO_CONTROL_URL is unset or blank", () => {
	expect(readControlPlaneUrl({})).toBe(DEFAULT_CONTROL_PLANE_URL);
	expect(readControlPlaneUrl({ AO_CONTROL_URL: "   " })).toBe(DEFAULT_CONTROL_PLANE_URL);
});

test("accepts a loopback control plane over plain HTTP and normalizes to an origin", () => {
	expect(readControlPlaneUrl({ AO_CONTROL_URL: "http://127.0.0.1:8080/" })).toBe("http://127.0.0.1:8080");
	expect(readControlPlaneUrl({ AO_CONTROL_URL: "http://localhost:8080" })).toBe("http://localhost:8080");
});

test("accepts an HTTPS control plane on any host", () => {
	expect(readControlPlaneUrl({ AO_CONTROL_URL: "https://staging.example.com" })).toBe("https://staging.example.com");
});

test("rejects plain HTTP off the loopback, so a typo cannot downgrade the login", () => {
	expect(() => readControlPlaneUrl({ AO_CONTROL_URL: "http://ao.agentlab.in" })).toThrow(/must be HTTPS/);
});

test("rejects a value that is not a bare origin", () => {
	expect(() => readControlPlaneUrl({ AO_CONTROL_URL: "ao.agentlab.in" })).toThrow(/absolute origin/);
	expect(() => readControlPlaneUrl({ AO_CONTROL_URL: "https://ao.agentlab.in/oauth" })).toThrow(/without a path/);
	expect(() => readControlPlaneUrl({ AO_CONTROL_URL: "https://user:pw@ao.agentlab.in" })).toThrow(/without a path/);
});
