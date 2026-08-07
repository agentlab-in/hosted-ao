import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DaemonStatus } from "../../shared/daemon-status";

const { selectMock, getStatusMock, onStatusMock } = vi.hoisted(() => ({
	selectMock: vi.fn(),
	getStatusMock: vi.fn(),
	onStatusMock: vi.fn(),
}));

vi.mock("./bridge", () => ({
	aoBridge: {
		machines: { select: (...args: unknown[]) => selectMock(...args) },
		daemon: {
			getStatus: (...args: unknown[]) => getStatusMock(...args),
			onStatus: (...args: unknown[]) => onStatusMock(...args),
		},
	},
}));

import { switchToMachine } from "./peer-session-switch";

let statusListener: ((status: DaemonStatus) => void) | undefined;

beforeEach(() => {
	vi.useFakeTimers();
	selectMock.mockReset().mockResolvedValue(undefined);
	getStatusMock.mockReset().mockResolvedValue({ state: "starting" } satisfies DaemonStatus);
	onStatusMock.mockReset().mockImplementation((listener: (status: DaemonStatus) => void) => {
		statusListener = listener;
		return () => {
			statusListener = undefined;
		};
	});
});

afterEach(() => {
	vi.useRealTimers();
});

describe("switchToMachine", () => {
	it("resolves ready once the daemon reports ready", async () => {
		const promise = switchToMachine("mch_1");
		await vi.waitFor(() => expect(onStatusMock).toHaveBeenCalled());

		statusListener?.({ state: "ready", port: 4000 });

		await expect(promise).resolves.toEqual({ status: "ready" });
		expect(selectMock).toHaveBeenCalledWith("mch_1");
	});

	it("resolves an error if selecting the machine throws", async () => {
		selectMock.mockRejectedValue(new Error("machine not found"));

		await expect(switchToMachine("mch_missing")).resolves.toEqual({ status: "error", message: "machine not found" });
		expect(onStatusMock).not.toHaveBeenCalled();
	});

	it("resolves an error after timing out with no ready status", async () => {
		const promise = switchToMachine("mch_1");
		await vi.waitFor(() => expect(onStatusMock).toHaveBeenCalled());

		await vi.advanceTimersByTimeAsync(10_000);

		await expect(promise).resolves.toEqual({
			status: "error",
			message: "Timed out waiting for the machine to become ready.",
		});
	});
});
