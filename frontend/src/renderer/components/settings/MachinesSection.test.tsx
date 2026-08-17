import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { HARNESS_SETUP_COMMAND, LOCAL_MACHINE_ID, localMachine, type AoMachine, type AoMachinesState } from "../../../shared/ao-machines";
import { MachinesSection } from "./MachinesSection";

const { refresh, select, writeText, pairedList, pairedRemove, probeFingerprint, getPinnedFingerprint, pairedAdd } =
	vi.hoisted(() => ({
		refresh: vi.fn(),
		select: vi.fn(),
		writeText: vi.fn(),
		pairedList: vi.fn(),
		pairedRemove: vi.fn(),
		probeFingerprint: vi.fn(),
		getPinnedFingerprint: vi.fn(),
		pairedAdd: vi.fn(),
	}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		machines: { refresh, select },
		clipboard: { writeText },
		pairedMachines: {
			list: pairedList,
			remove: pairedRemove,
			probeFingerprint,
			getPinnedFingerprint,
			add: pairedAdd,
		},
	},
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

const PAIRED_ONLINE: AoMachine = {
	id: "paired:192.168.1.5:8443",
	name: "192.168.1.5",
	baseUrl: "https://192.168.1.5:8443",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "unknown",
	harness: "unknown",
	harnessCommand: null,
};

const PAIRED_OFFLINE: AoMachine = {
	id: "paired:192.168.1.9:8443",
	name: "Pi in the closet",
	baseUrl: "https://192.168.1.9:8443",
	local: false,
	createdAt: null,
	lastSeen: "2020-01-01T09:00:00Z",
	reachability: "offline",
	harness: "unknown",
	harnessCommand: null,
};

beforeEach(() => {
	refresh.mockReset().mockResolvedValue(READY);
	select.mockReset().mockResolvedValue({ ...READY, activeMachineId: "mch_1" });
	writeText.mockReset().mockResolvedValue(undefined);
	pairedList.mockReset().mockResolvedValue([]);
	pairedRemove.mockReset().mockResolvedValue(undefined);
	probeFingerprint.mockReset();
	getPinnedFingerprint.mockReset();
	pairedAdd.mockReset();
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

test("readiness that is not reported is said once, not badged on every machine", async () => {
	refresh.mockResolvedValue({
		...READY,
		machines: [localMachine("This Mac"), remote({ id: "mch_9", name: "ao-unknown", baseUrl: "https://u.example.com" })],
	} satisfies AoMachinesState);
	renderSection();

	expect(await screen.findByTestId("ao-machines-harness-unknown")).toHaveTextContent(
		/not reported for remote machines/,
	);
	expect(within(machineRow("mch_9")).queryByText("No harness")).not.toBeInTheDocument();
	expect(within(machineRow("mch_9")).queryByTestId("ao-machine-harness-hint")).not.toBeInTheDocument();
});

test("nothing is said about readiness once every machine has reported", async () => {
	refresh.mockResolvedValue({
		...READY,
		machines: [localMachine("This Mac"), ONLINE, NO_HARNESS],
	} satisfies AoMachinesState);
	renderSection();

	await screen.findAllByTestId("ao-machine");
	expect(screen.queryByTestId("ao-machines-harness-unknown")).not.toBeInTheDocument();
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

// Regression: a failed refresh used to render nothing at all.
//
// The error line read `state?.error`, and on a rejected query `state` is
// undefined, so the reason never reached the pane. Combined with an unbounded
// request in the main process, that presented as a permanent "Looking for
// machines registered to your account..." spinner with no way forward. The
// main-process half is fixed in request-deadline.ts; this is the UI half.
test("shows the reason when the refresh fails, instead of spinning forever", async () => {
	refresh.mockRejectedValue(new Error("Listing machines timed out after 15s. The control plane may be unreachable."));

	renderSection();

	// Generous timeout on purpose: the pane retries once before reporting, so a
	// failure is expected to take a beat rather than appear instantly.
	const error = await screen.findByTestId("ao-machines-error", {}, { timeout: 5000 });
	expect(error).toHaveTextContent("Listing machines timed out after 15s.");
	// The spinner must be gone: it is what the user stared at for an hour.
	expect(screen.queryByText(/Looking for machines registered to your account/)).not.toBeInTheDocument();
});

test("a paired machine is listed and labelled by origin, distinct from hosted and local", async () => {
	pairedList.mockResolvedValue([PAIRED_ONLINE]);
	renderSection();
	await screen.findAllByTestId("ao-machine");

	const row = machineRow(PAIRED_ONLINE.id);
	expect(within(row).getByText(PAIRED_ONLINE.name)).toBeInTheDocument();
	expect(within(row).getByText("Paired")).toBeInTheDocument();
});

test("an unreachable paired machine stays listed, marked unreachable with its last-seen time", async () => {
	pairedList.mockResolvedValue([PAIRED_OFFLINE]);
	renderSection();
	await screen.findAllByTestId("ao-machine");

	const row = machineRow(PAIRED_OFFLINE.id);
	expect(within(row).getByText(PAIRED_OFFLINE.name)).toBeInTheDocument();
	expect(within(row).getByText("Offline")).toBeInTheDocument();
	expect(within(row).getByText(/Offline, last seen .+ ago/)).toBeInTheDocument();
});

test("removing a paired machine asks for confirmation, then calls remove and refreshes the list", async () => {
	pairedList.mockResolvedValue([PAIRED_ONLINE]);
	renderSection();
	await screen.findAllByTestId("ao-machine");

	const row = machineRow(PAIRED_ONLINE.id);
	await userEvent.click(within(row).getByRole("button", { name: /remove/i }));

	const confirmDialog = await screen.findByRole("dialog");
	expect(within(confirmDialog).getByText(/remove this paired machine/i)).toBeInTheDocument();

	pairedList.mockResolvedValue([]);
	await userEvent.click(within(confirmDialog).getByRole("button", { name: "Remove" }));

	expect(pairedRemove).toHaveBeenCalledWith(PAIRED_ONLINE.id);
	// The confirm dialog closes and the invalidated list refetches to empty.
	await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	await waitFor(() =>
		expect(screen.queryAllByTestId("ao-machine").some((r) => r.dataset.machineId === PAIRED_ONLINE.id)).toBe(false),
	);
	expect(machineRow(LOCAL_MACHINE_ID)).toBeTruthy();
});

test("clicking Add machine opens the pairing dialog", async () => {
	renderSection();
	await screen.findAllByTestId("ao-machine");

	await userEvent.click(screen.getByRole("button", { name: "Add machine" }));
	expect(await screen.findByTestId("pairing-dialog")).toBeInTheDocument();
});
