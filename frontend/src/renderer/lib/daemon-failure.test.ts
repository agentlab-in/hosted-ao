import { describe, expect, it } from "vitest";
import { daemonFailureHint, daemonFailureTitle } from "./daemon-failure";

describe("daemonFailureTitle", () => {
	it.each([
		{ code: "not_ready" as const, title: "AO daemon is not ready yet" },
		{ code: "port_unconfirmed" as const, title: "AO daemon is not ready yet" },
		{ code: "not_configured" as const, title: "AO daemon is not configured" },
		{ code: "daemon_unreachable" as const, title: "AO daemon is unreachable" },
		{ code: "identity_mismatch" as const, title: "AO daemon identity check failed" },
		{ code: "binary_missing" as const, title: "AO daemon binary is missing" },
		{ code: "spawn_failed" as const, title: "AO daemon failed to start" },
		{ code: "exited" as const, title: "AO daemon failed to start" },
		{ code: "machine_auth_failed" as const, title: "AO could not sign in to that machine" },
	])("returns $title for $code", ({ code, title }) => {
		expect(daemonFailureTitle({ state: "error", code })).toBe(title);
	});

	// The wrong hint is worse than a generic one: a machine the app has no
	// credential for is not fixed by setting AO_DAEMON_COMMAND, which is what
	// not_configured tells the user to do.
	it("points a credential failure at sign-in and the machine picker, not at AO_DAEMON_COMMAND", () => {
		const hint = daemonFailureHint({ state: "error", code: "machine_auth_failed" });
		expect(hint).toContain("Settings, Account");
		expect(hint).toContain("Settings, Machines");
		expect(hint).not.toContain("AO_DAEMON_COMMAND");
	});
});
