import { expect, test, vi } from "vitest";
import { LOCAL_MACHINE_ID, type AoMachine, type AoMachinesState } from "../shared/ao-machines";
import { createMachineSelection, type MachineSelectionDeps } from "./machine-selection";

const PASSCODE = "sup3rSecr3t";

const hostedMachine: AoMachine = {
	id: "mch_1",
	name: "ao-build-01",
	baseUrl: "https://vm.example.com",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
};

const pairedMachine: AoMachine = {
	id: "box_1",
	name: "Pi in the closet",
	baseUrl: "https://192.168.1.5:8443",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
};

function baseState(activeMachineId = LOCAL_MACHINE_ID): AoMachinesState {
	return {
		status: "ready",
		machines: [{ ...hostedMachine, id: LOCAL_MACHINE_ID, local: true, baseUrl: "" }, hostedMachine],
		activeMachineId,
	};
}

function harness(
	overrides: Partial<{ pinned: string | null; passcode: string | null; passcodeError: string }> = {},
) {
	const calls: string[] = [];
	let activeId = LOCAL_MACHINE_ID;

	const aoMachines: MachineSelectionDeps["aoMachines"] = {
		getState: vi.fn(() => baseState(activeId)),
		refresh: vi.fn(async () => {
			calls.push("aoMachines.refresh");
			return baseState(activeId);
		}),
		select: vi.fn(async (machineId: string) => {
			calls.push(`aoMachines.select:${machineId}`);
			activeId = machineId;
			return baseState(activeId);
		}),
	};

	const pairedMachines: MachineSelectionDeps["pairedMachines"] = {
		list: vi.fn(() => [pairedMachine]),
		getPinnedFingerprint: vi.fn(() => (overrides.pinned === undefined ? "AA:BB" : overrides.pinned)),
		getPasscode: vi.fn(async () => {
			if (overrides.passcodeError) throw new Error(overrides.passcodeError);
			return overrides.passcode === undefined ? PASSCODE : overrides.passcode;
		}),
	};

	const pairedTransport: MachineSelectionDeps["pairedTransport"] = {
		setMachine: vi.fn((machine) => {
			calls.push(`paired.setMachine:${machine?.id ?? "null"}`);
		}),
		token: vi.fn(() => PASSCODE),
	};

	const hostedTransport = {
		setMachine: vi.fn((machine: AoMachine | null) => {
			calls.push(`hosted.setMachine:${machine?.id ?? "null"}`);
		}),
		token: vi.fn(async (forceRefresh?: boolean) => `jwt${forceRefresh ? "-fresh" : ""}`),
	};

	const selection = createMachineSelection({
		aoMachines,
		pairedMachines,
		pairedTransport,
		getHostedTransport: () => hostedTransport,
	});

	return { selection, aoMachines, pairedMachines, pairedTransport, hostedTransport, calls };
}

test("selecting a paired machine points the renderer at its base URL", async () => {
	const { selection } = harness();
	const state = await selection.select("box_1");

	expect(state.activeMachineId).toBe("box_1");
	expect(state.machines.find((m) => m.id === "box_1")).toBeUndefined(); // not in the CP list; overlay is id-only, matching the picker's own paired query
	expect(selection.getState().activeMachineId).toBe("box_1");
});

test("the paired path sends the passcode as a bearer credential, and touches no control-plane call", async () => {
	const { selection, pairedTransport, aoMachines } = harness();
	await selection.select("box_1");

	expect(pairedTransport.setMachine).toHaveBeenCalledExactlyOnceWith(pairedMachine, PASSCODE);
	expect(await selection.gatewayToken()).toBe(PASSCODE);
	// getState() is documented no-network on ao-machines.ts and may be read for
	// the overlay; select/refresh are the network-touching methods and must
	// never fire for a purely paired selection.
	expect(aoMachines.select).not.toHaveBeenCalled();
	expect(aoMachines.refresh).not.toHaveBeenCalled();
});

test("selecting a hosted machine still uses the JWT credential path, unchanged", async () => {
	const { selection, aoMachines, hostedTransport } = harness();
	const state = await selection.select("mch_1");

	expect(aoMachines.select).toHaveBeenCalledExactlyOnceWith("mch_1");
	expect(state.activeMachineId).toBe("mch_1");
	expect(selection.isPairedActive()).toBe(false);
	expect(await selection.gatewayToken(true)).toBe("jwt-fresh");
	expect(hostedTransport.token).toHaveBeenCalledExactlyOnceWith(true);
});

test("selecting local still works and clears the active machine", async () => {
	const { selection, aoMachines } = harness();
	await selection.select("box_1");
	expect(selection.isPairedActive()).toBe(true);

	const state = await selection.select(LOCAL_MACHINE_ID);

	expect(aoMachines.select).toHaveBeenCalledExactlyOnceWith(LOCAL_MACHINE_ID);
	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
	expect(selection.isPairedActive()).toBe(false);
});

test("switching from paired to hosted parks the paired transport and hands off cleanly", async () => {
	const { selection, calls } = harness();
	await selection.select("box_1");
	calls.length = 0;
	await selection.select("mch_1");

	// Paired is torn down before the hosted select is allowed to publish.
	expect(calls).toEqual(["paired.setMachine:null", "aoMachines.select:mch_1"]);
});

test("switching from hosted to paired parks the hosted transport before activating paired", async () => {
	const { selection, calls } = harness();
	await selection.select("mch_1");
	calls.length = 0;
	await selection.select("box_1");

	expect(calls).toEqual(["hosted.setMachine:null", "paired.setMachine:box_1"]);
});

test("selecting paired works when no hosted transport exists yet (no control-plane credential in this install)", async () => {
	const { aoMachines, pairedMachines, pairedTransport } = harness();
	const selection = createMachineSelection({
		aoMachines,
		pairedMachines,
		pairedTransport,
		getHostedTransport: () => null,
	});

	const state = await selection.select("box_1");
	expect(pairedTransport.setMachine).toHaveBeenCalledExactlyOnceWith(pairedMachine, PASSCODE);
	expect(state.activeMachineId).toBe("box_1");
});

test("a paired machine with no pinned fingerprint cannot be selected into a working connection", async () => {
	const { selection, pairedTransport } = harness({ pinned: null });
	const state = await selection.select("box_1");

	expect(pairedTransport.setMachine).not.toHaveBeenCalled();
	expect(selection.isPairedActive()).toBe(false);
	expect(state.error).toMatch(/no accepted fingerprint/);
	expect(state.activeMachineId).toBe(LOCAL_MACHINE_ID);
});

test("a paired machine with no stored passcode cannot be selected into a working connection", async () => {
	const { selection, pairedTransport } = harness({ passcode: null });
	const state = await selection.select("box_1");

	expect(pairedTransport.setMachine).not.toHaveBeenCalled();
	expect(selection.isPairedActive()).toBe(false);
	expect(state.error).toMatch(/no stored passcode/);
});

test("a passcode decrypt failure is reported without ever including the passcode", async () => {
	const { selection } = harness({
		passcodeError: "This paired machine's stored passcode could not be decrypted on this machine. Re-pair it.",
	});
	const state = await selection.select("box_1");

	expect(state.error).toBeDefined();
	expect(state.error).not.toContain(PASSCODE);
	expect(JSON.stringify(state)).not.toContain(PASSCODE);
});

test("gatewayToken() routes to whichever transport is active", async () => {
	const { selection, hostedTransport, pairedTransport } = harness();
	await selection.select("mch_1");
	expect(await selection.gatewayToken()).toBe("jwt");
	expect(hostedTransport.token).toHaveBeenCalled();

	await selection.select("box_1");
	expect(await selection.gatewayToken()).toBe(PASSCODE);
	expect(pairedTransport.token).toHaveBeenCalled();
});
