import { describe, expect, test, vi } from "vitest";
import {
	HARNESS_SETUP_COMMAND,
	LOCAL_MACHINE_ID,
	harnessFromDoctorChecks,
	formatLastSeen,
	localMachine,
	parseMachineOrigin,
	parseMachinesResponse,
} from "./ao-machines";

const row = (extra: Record<string, unknown> = {}) => ({
	id: "mch_1",
	name: "ao-build-01",
	public_url: "https://vm.example.com",
	created_at: "2026-07-01T10:00:00Z",
	last_seen: "2026-07-30T09:00:00Z",
	...extra,
});

describe("parseMachinesResponse", () => {
	test("reads the control plane's list shape", () => {
		expect(parseMachinesResponse({ machines: [row()] })).toEqual([
			{
				id: "mch_1",
				name: "ao-build-01",
				baseUrl: "https://vm.example.com",
				local: false,
				createdAt: "2026-07-01T10:00:00Z",
				lastSeen: "2026-07-30T09:00:00Z",
				reachability: "unknown",
				harness: "unknown",
				harnessCommand: null,
			},
		]);
	});

	test("a machine with no harness reports the command the doctor check names", () => {
		const [machine] = parseMachinesResponse({
			machines: [
				row({
					doctor: {
						checks: [
							{ level: "PASS", section: "Core", name: "daemon" },
							{
								level: "WARN",
								section: "Agent harnesses",
								name: "claude-auth",
								message: "claude is installed but not signed in",
								remediation: "ao vm setup-harness claude",
							},
						],
					},
				}),
			],
		});
		expect(machine.harness).toBe("missing");
		expect(machine.harnessCommand).toBe("ao vm setup-harness claude");
	});

	test("a signed-in harness is ready and carries no command", () => {
		const [machine] = parseMachinesResponse({
			machines: [row({ doctor: { checks: [{ level: "PASS", name: "claude-auth" }] } })],
		});
		expect(machine).toMatchObject({ harness: "ready", harnessCommand: null });
	});

	// `ao doctor` has no HTTP route yet, so this is what every registered
	// machine looks like today. Absent must never read as "missing" or "ready".
	test("readiness that is simply not available degrades to unknown", () => {
		for (const doctor of [undefined, null, {}, { checks: [] }, { checks: [{ level: "PASS", name: "codex" }] }]) {
			const [machine] = parseMachinesResponse({ machines: [row({ doctor })] });
			expect(machine).toMatchObject({ harness: "unknown", harnessCommand: null });
		}
	});

	test("last_seen is optional, and a machine that has never connected reports null", () => {
		const [machine] = parseMachinesResponse({ machines: [row({ last_seen: null })] });
		expect(machine.lastSeen).toBeNull();
	});

	test("falls back to the host when the control plane has no name for a machine", () => {
		const [machine] = parseMachinesResponse({ machines: [row({ name: "  " })] });
		expect(machine.name).toBe("vm.example.com");
	});

	test("drops rows the app could not point itself at, keeping the rest", () => {
		const body = {
			machines: [
				row({ id: "mch_ok" }),
				row({ id: "", public_url: "https://a.example.com" }),
				row({ id: "mch_path", public_url: "https://b.example.com/api" }),
				row({ id: "mch_scheme", public_url: "ftp://c.example.com" }),
				row({ id: "mch_creds", public_url: "https://user:pw@d.example.com" }),
				// "local" is machine zero's id and can never come from the network.
				row({ id: LOCAL_MACHINE_ID, public_url: "https://e.example.com" }),
				"not an object",
			],
		};
		expect(parseMachinesResponse(body).map((machine) => machine.id)).toEqual(["mch_ok"]);
	});

	test("a body without a machines array is an empty list, not a throw", () => {
		for (const body of [null, undefined, {}, { machines: null }, { machines: "nope" }]) {
			expect(parseMachinesResponse(body)).toEqual([]);
		}
	});

	// A dropped row used to just vanish: the caller had no way to tell "the
	// account has no more machines" apart from "one row failed to parse". That
	// ambiguity is exactly what makes the absence-corroboration streak in
	// ao-machines.ts dangerous, so every drop must leave a trace.
	test("a dropped row is diagnosable, not just silently absent", () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
		try {
			const result = parseMachinesResponse({
				machines: [row({ id: "mch_ok" }), row({ id: "", public_url: "https://a.example.com" })],
			});
			expect(result.map((machine) => machine.id)).toEqual(["mch_ok"]);
			expect(warn).toHaveBeenCalledTimes(1);
			expect(warn.mock.calls[0]?.[0]).toMatch(/dropped a machine row/);
		} finally {
			warn.mockRestore();
		}
	});
});

describe("parseMachineOrigin", () => {
	test("accepts a bare host and an origin, and strips a trailing slash", () => {
		expect(parseMachineOrigin("vm.example.com")).toBe("https://vm.example.com");
		expect(parseMachineOrigin("https://vm.example.com/")).toBe("https://vm.example.com");
		expect(parseMachineOrigin(" https://vm.example.com ")).toBe("https://vm.example.com");
	});

	test("rejects anything that is not a bare origin", () => {
		for (const raw of ["", "   ", "https://vm.example.com/api", "https://vm.example.com?x=1", "ftp://vm", 7, null]) {
			expect(parseMachineOrigin(raw)).toBeNull();
		}
	});

	// Same rule as AO_CONTROL_URL. A machine row with an http public_url would
	// otherwise become the app's API base URL over a readable network, and
	// isRemoteDaemonBaseUrl would stop treating it as remote at the same time.
	test("allows plain HTTP only for a gateway on this machine", () => {
		expect(parseMachineOrigin("http://vm.example.com")).toBeNull();
		expect(parseMachineOrigin("http://10.0.0.4:8080")).toBeNull();
		expect(parseMachineOrigin("http://127.0.0.1:8080")).toBe("http://127.0.0.1:8080");
		expect(parseMachineOrigin("http://localhost:8080")).toBe("http://localhost:8080");
		expect(parseMachineOrigin("http://[::1]:8080")).toBe("http://[::1]:8080");
	});
});

describe("localMachine", () => {
	test("is machine zero, needs no account, and is reachable by definition", () => {
		expect(localMachine("This Mac")).toMatchObject({
			id: LOCAL_MACHINE_ID,
			name: "This Mac",
			local: true,
			baseUrl: "",
			reachability: "online",
		});
	});
});

describe("formatLastSeen", () => {
	const now = Date.parse("2026-07-30T12:00:00Z");

	test("reports how long ago the control plane last saw the machine", () => {
		expect(formatLastSeen("2026-07-30T11:57:00Z", now)).toMatch(/3 minutes ago/);
		expect(formatLastSeen("2026-07-30T09:00:00Z", now)).toMatch(/3 hours ago/);
		expect(formatLastSeen("2026-07-26T12:00:00Z", now)).toMatch(/4 days ago/);
	});

	test("has nothing to say about a machine that has never been seen", () => {
		expect(formatLastSeen(null, now)).toBeNull();
		expect(formatLastSeen("not a date", now)).toBeNull();
	});
});

describe("harnessFromDoctorChecks", () => {
	const warn = (extra: Record<string, unknown> = {}) => [
		{ level: "WARN", section: "Agent harnesses", name: "claude-auth", ...extra },
	];

	test("every non-PASS level is a missing harness, since the check never fails", () => {
		// The check emits WARN for a missing binary, a missing login, and an
		// unreadable answer alike, and deliberately never FAIL.
		expect(harnessFromDoctorChecks(warn())).toMatchObject({ harness: "missing" });
		expect(harnessFromDoctorChecks([{ level: "FAIL", name: "claude-auth" }])).toMatchObject({ harness: "missing" });
	});

	test("falls back to the documented command only when the check names none", () => {
		expect(harnessFromDoctorChecks(warn({ remediation: "  " })).harnessCommand).toBe(HARNESS_SETUP_COMMAND);
		expect(harnessFromDoctorChecks(warn({ remediation: "ao vm setup-harness claude --force" })).harnessCommand).toBe(
			"ao vm setup-harness claude --force",
		);
	});

	test("accepts a bare checks array as well as the whole doctor report", () => {
		expect(harnessFromDoctorChecks(warn())).toMatchObject({ harness: "missing" });
		expect(harnessFromDoctorChecks({ ok: true, failures: 0, checks: warn() })).toMatchObject({ harness: "missing" });
	});

	test("says nothing when there is no report to read", () => {
		for (const raw of [undefined, null, "PASS", 7, { checks: "nope" }]) {
			expect(harnessFromDoctorChecks(raw)).toMatchObject({ harness: "unknown", harnessCommand: null });
		}
	});
});
