import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { HARNESS_SETUP_COMMAND, LOCAL_MACHINE_ID, localMachine, type AoMachine, type AoMachinesState } from "../../../shared/ao-machines";
import { MachinesSection } from "./MachinesSection";

const { refresh, select, writeText } = vi.hoisted(() => ({
	refresh: vi.fn(),
	select: vi.fn(),
	writeText: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: { machines: { refresh, select }, clipboard: { writeText } },
}));

const remote = (extra: Partial<AoMachine> & Pick<AoMachine, "id" | "name" | "baseUrl">): AoMachine => ({
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
	...extra,
});

const ONLINE = remote({ id: "mch_1", name: "ao-build-01", baseUrl: "https://vm.example.com", harness: "ready" });
const NO_HARNESS = remote({
	id: "mch_2",
	name: "ao-scratch",
	baseUrl: "https://scratch.example.com",
	harness: "missing",
	harnessCommand: HARNESS_SETUP_COMMAND,
});
const OFFLINE = remote({
	id: "mch_3",
	name: "ao-eu-west",
	baseUrl: "https://eu-west.example.com",
	reachability: "offline",
	lastSeen: "2020-01-01T09:00:00Z",
});

const READY: AoMachinesState = {
	status: "ready",
	machines: [localMachine("This Mac"), ONLINE, NO_HARNESS, OFFLINE],
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

const machineRow = (id: string) => screen.getAllByTestId("ao-machine").find((row) => row.dataset.machineId === id)!;

beforeEach(() => {
	refresh.mockReset().mockResolvedValue(READY);
	select.mockReset().mockResolvedValue({ ...READY, activeMachineId: "mch_1" });
	writeText.mockReset().mockResolvedValue(undefined);
});

test("this computer is machine zero, listed first and active by default", async () => {
	renderSection();
	const rows = await screen.findAllByTestId("ao-machine");
	expect(rows[0].dataset.machineId).toBe(LOCAL_MACHINE_ID);
	expect(within(rows[0]).getByText("This Mac")).toBeInTheDocument();
	expect(within(rows[0]).getByText("Active")).toBeInTheDocument();
});

test("signed out still lists this computer and says an account is only for remote machines", async () => {
	refresh.mockResolvedValue({
		status: "signed-out",
		machines: [localMachine("This Mac")],
		activeMachineId: LOCAL_MACHINE_ID,
	} satisfies AoMachinesState);
	renderSection();

	expect(await screen.findByText("This Mac")).toBeInTheDocument();
	expect(screen.getByText(/works without an account/i)).toBeInTheDocument();
});

test("an unreachable machine says it is offline and when it was last seen", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	const row = machineRow("mch_3");

	expect(within(row).getByText("Offline")).toBeInTheDocument();
	expect(within(row).getByText(/Offline, last seen .+ ago/)).toBeInTheDocument();
});

test("a machine with no harness shows exactly which command to run, and copies it", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	const row = machineRow("mch_2");

	const hint = within(row).getByTestId("ao-machine-harness-hint");
	expect(hint).toHaveTextContent(`No agent harness on ao-scratch. Run ${HARNESS_SETUP_COMMAND} on that machine.`);

	await userEvent.click(within(hint).getByRole("button", { name: /copy/i }));
	expect(writeText).toHaveBeenCalledWith(HARNESS_SETUP_COMMAND);
});

test("a machine that is up and set up carries no state badge at all", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");
	const row = machineRow("mch_1");

	expect(within(row).queryByText("Offline")).not.toBeInTheDocument();
	expect(within(row).queryByText("No harness")).not.toBeInTheDocument();
	expect(within(row).queryByTestId("ao-machine-harness-hint")).not.toBeInTheDocument();
});

test("picking a machine switches to it, and the active one cannot be re-picked", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");

	await userEvent.click(within(machineRow("mch_1")).getByRole("button"));
	expect(select).toHaveBeenCalledWith("mch_1");

	expect(await within(machineRow("mch_1")).findByText("Active")).toBeInTheDocument();
	expect(within(machineRow("mch_1")).getByRole("button")).toBeDisabled();
});

test("a failure to list is shown, and this computer stays available", async () => {
	refresh.mockResolvedValue({
		status: "error",
		machines: [localMachine("This Mac")],
		activeMachineId: LOCAL_MACHINE_ID,
		error: "The control plane returned 503 listing machines.",
	} satisfies AoMachinesState);
	renderSection();

	expect(await screen.findByTestId("ao-machines-error")).toHaveTextContent(/503 listing machines/);
	expect(within(machineRow(LOCAL_MACHINE_ID)).getByText("This Mac")).toBeInTheDocument();
});
