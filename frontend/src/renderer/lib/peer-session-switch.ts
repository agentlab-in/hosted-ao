import type { DaemonStatus } from "../../shared/daemon-status";
import { aoBridge } from "./bridge";

// Demo-facing: a hung switch must not spin forever. If the daemon has not
// reported ready by this point, treat it as a failure and let the user retry.
const SWITCH_TIMEOUT_MS = 10_000;

export type MachineSwitchResult = { status: "ready" } | { status: "error"; message: string };

/**
 * Click-to-switch for a peer session row: select the machine (reusing the
 * same bridge call MachinesSection uses), then wait for the daemon to report
 * ready before the caller navigates. Never resolves "ready" for a machine
 * that didn't actually come up.
 */
export async function switchToMachine(machineId: string): Promise<MachineSwitchResult> {
	try {
		await aoBridge.machines.select(machineId);
	} catch (err) {
		return { status: "error", message: err instanceof Error ? err.message : "Could not switch machines." };
	}
	return waitForDaemonReady();
}

function waitForDaemonReady(): Promise<MachineSwitchResult> {
	return new Promise((resolve) => {
		let settled = false;
		let stopListening = () => {};
		const finish = (result: MachineSwitchResult) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			stopListening();
			resolve(result);
		};
		const fromStatus = (status: DaemonStatus) => {
			if (status.state === "ready") finish({ status: "ready" });
			else if (status.state === "error") {
				finish({ status: "error", message: status.message ?? "The machine did not become ready." });
			}
		};
		const timer = setTimeout(
			() => finish({ status: "error", message: "Timed out waiting for the machine to become ready." }),
			SWITCH_TIMEOUT_MS,
		);
		stopListening = aoBridge.daemon.onStatus(fromStatus);
		// The status may already have settled before onStatus's next event fires.
		void aoBridge.daemon.getStatus().then(fromStatus);
	});
}
