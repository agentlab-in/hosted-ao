import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { LOCAL_MACHINE_ID, localMachine, type AoMachine, type AoMachinesState } from "../../../shared/ao-machines";
import { MachinesSection } from "./MachinesSection";

const { refresh, select, pairedRemove, probeFingerprint, getPinnedFingerprint, pairedAdd } = vi.hoisted(() => ({
	refresh: vi.fn(),
	select: vi.fn(),
	pairedRemove: vi.fn(),
	probeFingerprint: vi.fn(),
	getPinnedFingerprint: vi.fn(),
	pairedAdd: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		machines: { refresh, select },
		clipboard: { writeText: vi.fn() },
		pairedMachines: {
			refresh: vi.fn(),
			remove: pairedRemove,
			probeFingerprint,
			getPinnedFingerprint,
			add: pairedAdd,
		},
	},
}));

const PAIRED: AoMachine = {
	id: "paired:192.168.1.5:8443",
	name: "Build box",
	baseUrl: "https://192.168.1.5:8443",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
};

const READY: AoMachinesState = {
	status: "ready",
	machines: [localMachine("This Mac"), PAIRED],
	activeMachineId: LOCAL_MACHINE_ID,
};

function renderSection() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<MachinesSection />
		</QueryClientProvider>,
	);
}

const machineRow = (id: string) =>
	screen.getAllByTestId("ao-machine").find((row) => row.dataset.machineId === id)!;

beforeEach(() => {
	refresh.mockReset().mockResolvedValue(READY);
	select.mockReset().mockImplementation(async (id: string) => ({ ...READY, activeMachineId: id }));
	pairedRemove.mockReset().mockResolvedValue(undefined);
	probeFingerprint.mockReset();
	getPinnedFingerprint.mockReset();
	pairedAdd.mockReset();
});

test("lists this computer first and active", async () => {
	renderSection();
	const rows = await screen.findAllByTestId("ao-machine");
	expect(rows[0].dataset.machineId).toBe(LOCAL_MACHINE_ID);
	expect(within(rows[0]).getByText("This Mac")).toBeInTheDocument();
	expect(within(rows[0]).getByText("Active")).toBeInTheDocument();
});

test("lists paired machines from the unified machine state", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	const row = machineRow(PAIRED.id);
	expect(within(row).getByText(PAIRED.name)).toBeInTheDocument();
	expect(within(row).getByText("Paired")).toBeInTheDocument();
});

test("selects a paired machine through the atomic machine-selection bridge", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	await userEvent.click(within(machineRow(PAIRED.id)).getByRole("button", { name: new RegExp(PAIRED.name) }));
	expect(select).toHaveBeenCalledWith(PAIRED.id);
	expect(await within(machineRow(PAIRED.id)).findByText("Active")).toBeInTheDocument();
});

test("shows unified refresh failures without hiding the local machine", async () => {
	refresh.mockResolvedValue({
		status: "error",
		machines: [localMachine("This Mac")],
		activeMachineId: LOCAL_MACHINE_ID,
		error: "Checking paired machines failed.",
	} satisfies AoMachinesState);
	renderSection();
	expect(await screen.findByTestId("ao-machines-error")).toHaveTextContent("Checking paired machines failed.");
	expect(within(machineRow(LOCAL_MACHINE_ID)).getByText("This Mac")).toBeInTheDocument();
});

test("removes a paired machine after confirmation and refreshes unified state", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	await userEvent.click(within(machineRow(PAIRED.id)).getByRole("button", { name: "Remove" }));
	const dialog = await screen.findByRole("dialog");
	refresh.mockResolvedValue({ ...READY, machines: [localMachine("This Mac")] });
	await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
	expect(pairedRemove).toHaveBeenCalledWith(PAIRED.id);
	await waitFor(() => expect(machineRow(PAIRED.id)).toBeUndefined());
});

test("opens the account-free pairing dialog", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	await userEvent.click(screen.getByRole("button", { name: "Add machine" }));
	expect(await screen.findByTestId("pairing-dialog")).toBeInTheDocument();
});
