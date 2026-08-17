import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { AddPairedMachineDialog } from "./AddPairedMachineDialog";

const { probeFingerprint, getPinnedFingerprint, add } = vi.hoisted(() => ({
	probeFingerprint: vi.fn(),
	getPinnedFingerprint: vi.fn(),
	add: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		pairedMachines: { probeFingerprint, getPinnedFingerprint, add, list: vi.fn(), remove: vi.fn() },
	},
}));

const FINGERPRINT = "DF:9A:6C:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";
const OTHER_FINGERPRINT = "AA:BB:CC:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";

function renderDialog(onOpenChange = vi.fn(), onPaired = vi.fn()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(
		<QueryClientProvider client={client}>
			<AddPairedMachineDialog open={true} onOpenChange={onOpenChange} onPaired={onPaired} />
		</QueryClientProvider>,
	);
	return { onOpenChange, onPaired };
}

async function fillForm(passcode = "abc123XY") {
	await userEvent.type(screen.getByLabelText("Address"), "192.168.1.5");
	await userEvent.type(screen.getByLabelText("Port"), "8443");
	await userEvent.type(screen.getByLabelText("Passcode"), passcode);
	return passcode;
}

beforeEach(() => {
	probeFingerprint.mockReset();
	getPinnedFingerprint.mockReset().mockResolvedValue(null);
	add.mockReset();
});

test("happy path: enter address and passcode, see the fingerprint, accept, and the machine is added", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	add.mockResolvedValue({
		id: "paired:192.168.1.5:8443",
		name: "192.168.1.5",
		baseUrl: "https://192.168.1.5:8443",
		local: false,
		createdAt: null,
		lastSeen: null,
		reachability: "unknown",
		harness: "unknown",
		harnessCommand: null,
	});
	const { onOpenChange, onPaired } = renderDialog();

	await fillForm("abc123XY");
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	expect(await screen.findByTestId("pairing-step-compare")).toBeInTheDocument();
	expect(probeFingerprint).toHaveBeenCalledWith("192.168.1.5", 8443);
	// Rendered so it can actually be eyeballed: split into rows, not one long line.
	const block = screen.getByTestId("pairing-fingerprint");
	expect(block.textContent).toContain("DF:9A:6C:0D:63:16:53:39");
	expect(block.querySelectorAll("span").length).toBeGreaterThan(1);

	await userEvent.click(screen.getByRole("button", { name: "Pair machine" }));

	await waitFor(() =>
		expect(add).toHaveBeenCalledWith({
			id: "paired:192.168.1.5:8443",
			name: "192.168.1.5",
			address: "192.168.1.5",
			port: 8443,
			passcode: "abc123XY",
			fingerprint: FINGERPRINT,
		}),
	);
	expect(onPaired).toHaveBeenCalled();
	expect(onOpenChange).toHaveBeenCalledWith(false);
});

test("declining at the comparison step pins nothing and adds nothing", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	const { onOpenChange } = renderDialog();

	await fillForm();
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	expect(await screen.findByTestId("pairing-step-compare")).toBeInTheDocument();

	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

	expect(add).not.toHaveBeenCalled();
	expect(onOpenChange).toHaveBeenCalledWith(false);
});

test("a probe error renders as an error, not a successful pairing", async () => {
	probeFingerprint.mockResolvedValue({ error: "No certificate could be retrieved from that address." });
	renderDialog();

	await fillForm();
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	const errorStep = await screen.findByTestId("pairing-step-error");
	expect(errorStep).toHaveTextContent("No certificate could be retrieved from that address.");
	expect(screen.queryByTestId("pairing-step-compare")).not.toBeInTheDocument();
	expect(add).not.toHaveBeenCalled();
});

test("a fingerprint mismatch is a hard refusal with a re-pair action and no connect-anyway affordance", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	getPinnedFingerprint.mockResolvedValue(OTHER_FINGERPRINT);
	renderDialog();

	await fillForm();
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	const mismatchStep = await screen.findByTestId("pairing-step-mismatch");
	expect(mismatchStep).toHaveTextContent(/does not match the one already pinned/i);
	expect(mismatchStep).toHaveTextContent(/box was replaced/i);

	// No accept / connect-anyway affordance anywhere in this state: the only
	// buttons offered are the generic Cancel/Close and the explicit re-pair,
	// there is no shortcut button that trusts the mismatched fingerprint.
	expect(screen.queryByRole("button", { name: "Pair machine" })).not.toBeInTheDocument();
	expect(screen.queryByRole("button", { name: /connect anyway/i })).not.toBeInTheDocument();

	// The only forward action is the explicit re-pair, which requires a fresh,
	// deliberate accept afterward rather than pinning the new fingerprint itself.
	const rePairButton = screen.getByRole("button", { name: "Re-pair this machine" });
	await userEvent.click(rePairButton);

	expect(await screen.findByTestId("pairing-step-compare")).toBeInTheDocument();
	expect(add).not.toHaveBeenCalled();
});

test("the passcode never appears in the DOM after submission", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	renderDialog();

	const passcode = await fillForm("s3cr3t-passcode");
	expect(screen.getByDisplayValue(passcode)).toBeInTheDocument();

	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-compare");

	expect(screen.queryByDisplayValue(passcode)).not.toBeInTheDocument();
	expect(screen.queryByText(passcode)).not.toBeInTheDocument();
	expect(document.querySelector('input[type="password"]')).not.toBeInTheDocument();
});
