import { describe, expect, test } from "vitest";
import {
	HARNESS_SETUP_COMMAND,
	LOCAL_MACHINE_ID,
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

	test("a machine with no harness reports the exact command to run", () => {
		const [machine] = parseMachinesResponse({ machines: [row({ harness: { ready: false } })] });
		expect(machine.harness).toBe("missing");
		expect(machine.harnessCommand).toBe(HARNESS_SETUP_COMMAND);
	});

	test("a harness command the control plane supplies wins over the default", () => {
		const [machine] = parseMachinesResponse({
			machines: [row({ harness: { ready: false, command: "ao vm setup-harness codex" } })],
		});
		expect(machine.harnessCommand).toBe("ao vm setup-harness codex");
	});

	test("a ready harness carries no command", () => {
		const [machine] = parseMachinesResponse({ machines: [row({ harness: { ready: true } })] });
		expect(machine).toMatchObject({ harness: "ready", harnessCommand: null });
	});

	// Readiness comes from task 11's `ao doctor` surfacing, which may not be on
	// this route yet. Absent must never be read as "missing" or as "ready".
	test("an absent harness field degrades to unknown rather than claiming either state", () => {
		for (const harness of [undefined, null, {}, "ready", { ready: "yes" }]) {
			const [machine] = parseMachinesResponse({ machines: [row({ harness })] });
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
