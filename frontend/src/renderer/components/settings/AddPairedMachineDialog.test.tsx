import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, expect, test, vi } from "vitest";
import { AddPairedMachineDialog } from "./AddPairedMachineDialog";

const { probeFingerprint, getPinnedFingerprint, add, remove } = vi.hoisted(() => ({
	probeFingerprint: vi.fn(),
	getPinnedFingerprint: vi.fn(),
	add: vi.fn(),
	remove: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		pairedMachines: { probeFingerprint, getPinnedFingerprint, add, list: vi.fn(), remove },
	},
}));

// Real by default (the factory below wires it to the actual implementation),
// overridable per test so the "match resolves, but this attempt's signal was
// already aborted" branch can be constructed deterministically without
// fighting real microtask ordering (racePairAddresses's own settled-guard
// otherwise always makes a same-tick abort win over a same-tick match, which
// is correct in production but makes that exact interleaving untestable
// through the real implementation).
const { racePairAddressesMock } = vi.hoisted(() => ({ racePairAddressesMock: vi.fn() }));

vi.mock("../../../shared/pair-race", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../../shared/pair-race")>();
	racePairAddressesMock.mockImplementation(actual.racePairAddresses);
	return { ...actual, racePairAddresses: racePairAddressesMock };
});

const FINGERPRINT = "DF:9A:6C:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";
const OTHER_FINGERPRINT = "AA:BB:CC:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";
const FINGERPRINT_HEX = FINGERPRINT.replace(/:/g, "").toLowerCase();

/** A syntactically valid `ao-pair://` string (frontend/src/shared/pair-string.ts's
 * grammar) so paste-flow tests exercise the real parser rather than a stub. */
function buildPairString(addrs: string, passcode = "abc123XY"): string {
	return `ao-pair://v1/${addrs}#${FINGERPRINT_HEX}:${passcode}`;
}

const addedMachine = (overrides: Partial<{ id: string; name: string; baseUrl: string }> = {}) => ({
	id: "paired:192.168.1.5:8443",
	name: "192.168.1.5",
	baseUrl: "https://192.168.1.5:8443",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "unknown" as const,
	harness: "unknown" as const,
	harnessCommand: null,
	...overrides,
});

function renderDialog(onOpenChange = vi.fn(), onPaired = vi.fn()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(
		<QueryClientProvider client={client}>
			<AddPairedMachineDialog open={true} onOpenChange={onOpenChange} onPaired={onPaired} />
		</QueryClientProvider>,
	);
	return { onOpenChange, onPaired };
}

/** Unlike renderDialog above, `open` here is real state a dismissal (Cancel,
 * the close button, Escape, an overlay click) actually flips, the way the
 * real caller (MachinesSection) wires it -- needed for tests where the point
 * is what happens to in-flight work once the dialog is actually dismissed
 * (and, via `reopen`, re-opened). */
function renderControlledDialog(onPaired = vi.fn()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	const controls: { setOpen?: (open: boolean) => void } = {};
	function Controlled() {
		const [open, setOpen] = useState(true);
		controls.setOpen = setOpen;
		return <AddPairedMachineDialog open={open} onOpenChange={setOpen} onPaired={onPaired} />;
	}
	render(
		<QueryClientProvider client={client}>
			<Controlled />
		</QueryClientProvider>,
	);
	return { onPaired, reopen: () => act(() => controls.setOpen?.(true)) };
}

/** The dialog now opens on the paste-a-string step; the pre-existing
 * address/port/passcode form is reached through the manual escape hatch. */
async function goToManualForm() {
	await userEvent.click(screen.getByRole("button", { name: "Enter details manually" }));
	await screen.findByTestId("pairing-step-form");
}

async function fillForm(passcode = "abc123XY") {
	await userEvent.type(screen.getByLabelText("Address"), "192.168.1.5");
	await userEvent.type(screen.getByLabelText("Port"), "8443");
	await userEvent.type(screen.getByLabelText("Passcode"), passcode);
	return passcode;
}

async function pasteString(pairString: string) {
	await userEvent.click(screen.getByLabelText("Pairing string"));
	await userEvent.paste(pairString);
}

beforeEach(() => {
	probeFingerprint.mockReset();
	getPinnedFingerprint.mockReset().mockResolvedValue(null);
	add.mockReset();
	remove.mockReset().mockResolvedValue(undefined);
});

test("happy path: enter address and passcode, see the fingerprint, accept, and the machine is added", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	add.mockResolvedValue(addedMachine());
	const { onOpenChange, onPaired } = renderDialog();

	await goToManualForm();
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

	await goToManualForm();
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

	await goToManualForm();
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

	await goToManualForm();
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

	await goToManualForm();
	const passcode = await fillForm("s3cr3t-passcode");
	expect(screen.getByDisplayValue(passcode)).toBeInTheDocument();

	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-compare");

	expect(screen.queryByDisplayValue(passcode)).not.toBeInTheDocument();
	expect(screen.queryByText(passcode)).not.toBeInTheDocument();
	expect(document.querySelector('input[type="password"]')).not.toBeInTheDocument();
});

test("paste: races every address, a public one wins after the private one fails, and no compare step is shown", async () => {
	probeFingerprint.mockImplementation(async (host: string) => {
		if (host === "192.168.1.5") return { error: "No certificate could be retrieved from that address." };
		return { fingerprint: FINGERPRINT };
	});
	add.mockResolvedValue(addedMachine({ id: "paired:9.9.9.9:8443", name: "9.9.9.9", baseUrl: "https://9.9.9.9:8443" }));
	const { onOpenChange, onPaired } = renderDialog();

	await pasteString(buildPairString("192.168.1.5:8443,9.9.9.9:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	await waitFor(
		() =>
			expect(add).toHaveBeenCalledWith({
				id: "paired:9.9.9.9:8443",
				name: "9.9.9.9",
				address: "9.9.9.9",
				port: 8443,
				passcode: "abc123XY",
				fingerprint: FINGERPRINT,
				addresses: ["9.9.9.9:8443", "192.168.1.5:8443"],
			}),
		{ timeout: 2000 },
	);
	expect(onPaired).toHaveBeenCalled();
	expect(onOpenChange).toHaveBeenCalledWith(false);
	// The winning-path story never shows a visual compare step: the fingerprint
	// is auto-pinned from the pasted string itself, the out-of-band channel.
	expect(screen.queryByTestId("pairing-step-compare")).not.toBeInTheDocument();
});

test("paste: a wrong-fingerprint candidate is skipped silently and the race continues to a winner", async () => {
	probeFingerprint.mockImplementation(async (host: string) => {
		if (host === "192.168.1.5") return { fingerprint: OTHER_FINGERPRINT };
		return { fingerprint: FINGERPRINT };
	});
	add.mockResolvedValue(addedMachine({ id: "paired:192.168.1.6:8443", name: "192.168.1.6" }));
	const { onPaired } = renderDialog();

	await pasteString(buildPairString("192.168.1.5:8443,192.168.1.6:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	await waitFor(() => expect(onPaired).toHaveBeenCalled());
	expect(add).toHaveBeenCalledWith(
		expect.objectContaining({ address: "192.168.1.6", addresses: ["192.168.1.6:8443", "192.168.1.5:8443"] }),
	);
	// The mismatch is discovery noise, not a hard refusal: no mismatch UI, no alert.
	expect(screen.queryByTestId("pairing-step-mismatch")).not.toBeInTheDocument();
	expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

test("paste: garbage input shows an inline parse error and the manual entry path stays reachable", async () => {
	renderDialog();

	await pasteString("not a pairing string");
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	expect(await screen.findByRole("alert")).toHaveTextContent(/pairing string/i);
	expect(probeFingerprint).not.toHaveBeenCalled();
	expect(add).not.toHaveBeenCalled();

	await userEvent.click(screen.getByRole("button", { name: "Enter details manually" }));
	expect(await screen.findByTestId("pairing-step-form")).toBeInTheDocument();
});

test("paste: the pasted string never reappears in the DOM once submitted", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	add.mockResolvedValue(addedMachine());
	renderDialog();

	const pairString = buildPairString("192.168.1.5:8443");
	await pasteString(pairString);
	expect(screen.getByDisplayValue(pairString)).toBeInTheDocument();

	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	await waitFor(() => expect(add).toHaveBeenCalled());
	expect(screen.queryByDisplayValue(pairString)).not.toBeInTheDocument();
	expect(screen.queryByText(pairString)).not.toBeInTheDocument();
	expect(document.querySelector("textarea")).not.toBeInTheDocument();
	// The passcode embedded in the pasted string must not linger either.
	expect(screen.queryByText("abc123XY")).not.toBeInTheDocument();
});

test("paste: dismissing the dialog mid-race cancels it, so a later winner is never added", async () => {
	let resolveWinner: (v: { fingerprint: string } | { error: string }) => void = () => undefined;
	const winnerProbe = new Promise<{ fingerprint: string } | { error: string }>((resolve) => {
		resolveWinner = resolve;
	});
	probeFingerprint.mockImplementation(() => winnerProbe);
	const { onPaired } = renderControlledDialog();

	await pasteString(buildPairString("192.168.1.5:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-racing");

	// Cancel is no longer blocked while the race is in flight: dismissing here
	// must cancel the race, not just hide the dialog around it.
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

	// The probe that would have won only answers now, well after dismissal.
	resolveWinner({ fingerprint: FINGERPRINT });
	await new Promise((resolve) => setTimeout(resolve, 20));

	expect(add).not.toHaveBeenCalled();
	expect(onPaired).not.toHaveBeenCalled();
});

test("paste: switching to manual entry mid-race cancels it too", async () => {
	let resolveWinner: (v: { fingerprint: string } | { error: string }) => void = () => undefined;
	const winnerProbe = new Promise<{ fingerprint: string } | { error: string }>((resolve) => {
		resolveWinner = resolve;
	});
	probeFingerprint.mockImplementation(() => winnerProbe);
	const { onPaired } = renderControlledDialog();

	await pasteString(buildPairString("192.168.1.5:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-racing");

	await userEvent.click(screen.getByRole("button", { name: "Enter details manually" }));
	expect(await screen.findByTestId("pairing-step-form")).toBeInTheDocument();

	resolveWinner({ fingerprint: FINGERPRINT });
	await new Promise((resolve) => setTimeout(resolve, 20));

	expect(add).not.toHaveBeenCalled();
	expect(onPaired).not.toHaveBeenCalled();
	// Still on the manual form, not bounced onto some race-result step.
	expect(screen.getByTestId("pairing-step-form")).toBeInTheDocument();
});

test("paste: a match reported after this attempt's signal was already aborted still skips add()", async () => {
	// racePairAddresses is mocked for this one test so the "matched, but
	// signal.aborted by the time the dialog checks" branch can be built
	// directly, instead of chasing a same-tick timing race that the real
	// implementation's own settled-guard makes unreachable (a cancel that
	// really lands first always wins there, which is the correct, tested
	// behavior in pair-race.test.ts).
	let resolveRace: (outcome: { status: "matched"; host: string; port: number }) => void = () => undefined;
	const racePromise = new Promise<{ status: "matched"; host: string; port: number }>((resolve) => {
		resolveRace = resolve;
	});
	racePairAddressesMock.mockReturnValueOnce(racePromise);
	const { onPaired } = renderControlledDialog();

	await pasteString(buildPairString("192.168.1.5:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-racing");

	// Cancel this attempt while the (mocked) race is still outstanding: this
	// aborts the AbortController startPairing minted for it.
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

	// The race now reports a match anyway, as if it had been about to win the
	// instant before the cancel actually landed.
	resolveRace({ status: "matched", host: "192.168.1.5", port: 8443 });
	await new Promise((resolve) => setTimeout(resolve, 20));

	expect(add).not.toHaveBeenCalled();
	expect(onPaired).not.toHaveBeenCalled();
	expect(remove).not.toHaveBeenCalled();
});

test("paste: dismissal is blocked while finalizing, and the pairing completes rather than being cancellable", async () => {
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	let resolveAdd: (machine: ReturnType<typeof addedMachine>) => void = () => undefined;
	const addPromise = new Promise<ReturnType<typeof addedMachine>>((resolve) => {
		resolveAdd = resolve;
	});
	add.mockImplementation(() => addPromise);
	const { onPaired } = renderControlledDialog();

	await pasteString(buildPairString("192.168.1.5:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	await screen.findByTestId("pairing-step-finalizing");
	await waitFor(() => expect(add).toHaveBeenCalled());

	// Every dismissal path is a no-op while finalizing: Radix's own
	// Escape/pointer-outside dismissal is prevented, and Close/Cancel are
	// disabled so clicking them does nothing either.
	fireEvent.keyDown(document, { key: "Escape" });
	fireEvent.pointerDown(document.body);
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
	await userEvent.click(screen.getByRole("button", { name: "Close" }));

	expect(screen.getByTestId("pairing-step-finalizing")).toBeInTheDocument();
	expect(onPaired).not.toHaveBeenCalled();

	// add() finally resolves: the pairing simply completes, since this window
	// was never actually cancellable.
	resolveAdd(addedMachine());

	await waitFor(() => expect(onPaired).toHaveBeenCalled());
	expect(remove).not.toHaveBeenCalled();
});

test("paste: cancelling race A, reopening, and starting race B leaves A unable to complete", async () => {
	let resolveA: (v: { fingerprint: string } | { error: string }) => void = () => undefined;
	const probeA = new Promise<{ fingerprint: string } | { error: string }>((resolve) => {
		resolveA = resolve;
	});
	probeFingerprint.mockImplementationOnce(() => probeA);
	add.mockResolvedValue(addedMachine({ id: "paired:192.168.1.6:8443", name: "192.168.1.6" }));
	const { onPaired, reopen } = renderControlledDialog();

	// Race A: paste and submit, but its only address's probe never answers yet.
	await pasteString(buildPairString("192.168.1.5:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByTestId("pairing-step-racing");

	// Cancel A (dismiss), then reopen the dialog fresh.
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
	reopen();
	await screen.findByTestId("pairing-step-paste");

	// Race B: a different address that matches immediately.
	probeFingerprint.mockResolvedValue({ fingerprint: FINGERPRINT });
	await pasteString(buildPairString("192.168.1.6:8443"));
	await userEvent.click(screen.getByRole("button", { name: "Continue" }));

	await waitFor(() => expect(onPaired).toHaveBeenCalledTimes(1));
	expect(add).toHaveBeenCalledTimes(1);
	expect(add).toHaveBeenCalledWith(expect.objectContaining({ address: "192.168.1.6" }));

	// A's probe, abandoned long ago, finally answers as a match. With an
	// attempt-local signal this cannot revive A: it closed over its own
	// controller's signal at the time it started, not a shared ref B has
	// since moved on from.
	resolveA({ fingerprint: FINGERPRINT });
	await new Promise((resolve) => setTimeout(resolve, 20));

	// Still exactly one add()/onPaired, both from B; A contributed nothing.
	expect(add).toHaveBeenCalledTimes(1);
	expect(onPaired).toHaveBeenCalledTimes(1);
});
