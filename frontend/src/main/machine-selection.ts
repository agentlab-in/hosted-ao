import { LOCAL_MACHINE_ID, localMachine, type AoMachine, type AoMachinesState } from "../shared/ao-machines";
import type { PairedMachineTransport } from "./paired-machine-transport";

export type MachineSelectionPairedMachines = {
	list: () => AoMachine[];
	refresh: () => Promise<AoMachine[]>;
	getPinnedFingerprint: (id: string) => string | null;
	getPasscode: (id: string) => Promise<string | null>;
};

export type MachineSelectionDeps = {
	localMachineName: string;
	pairedMachines: MachineSelectionPairedMachines;
	pairedTransport: PairedMachineTransport;
	onLocalSelected: () => void;
};

export type MachineSelection = {
	getState: () => AoMachinesState;
	refresh: () => Promise<AoMachinesState>;
	select: (machineId: string) => Promise<AoMachinesState>;
	gatewayToken: () => Promise<string | null>;
	isPairedActive: () => boolean;
};

const errorMessage = (err: unknown): string => err instanceof Error ? err.message : String(err);

export function createMachineSelection(deps: MachineSelectionDeps): MachineSelection {
	let activePairedMachine: AoMachine | null = null;
	let error: string | undefined;

	function state(paired = deps.pairedMachines.list()): AoMachinesState {
		return {
			status: "ready",
			machines: [localMachine(deps.localMachineName), ...paired],
			activeMachineId: activePairedMachine?.id ?? LOCAL_MACHINE_ID,
			...(error ? { error } : {}),
		};
	}

	async function select(machineId: string): Promise<AoMachinesState> {
		if (machineId === LOCAL_MACHINE_ID) {
			activePairedMachine = null;
			error = undefined;
			deps.pairedTransport.setMachine(null, null);
			deps.onLocalSelected();
			return state();
		}

		const paired = deps.pairedMachines.list().find((machine) => machine.id === machineId);
		if (!paired) {
			error = "That paired machine is no longer available.";
			return state();
		}
		if (!deps.pairedMachines.getPinnedFingerprint(paired.id)) {
			error = `${paired.name} has no accepted fingerprint. Re-pair it before connecting.`;
			return state();
		}

		try {
			const passcode = await deps.pairedMachines.getPasscode(paired.id);
			if (!passcode) {
				error = `${paired.name} has no stored passcode. Re-pair it.`;
				return state();
			}
			activePairedMachine = paired;
			error = undefined;
			deps.pairedTransport.setMachine(paired, passcode);
		} catch (err) {
			error = errorMessage(err);
		}
		return state();
	}

	return {
		getState: () => state(),
		refresh: async () => {
			try {
				const paired = await deps.pairedMachines.refresh();
				if (activePairedMachine && !paired.some((machine) => machine.id === activePairedMachine?.id)) {
					await select(LOCAL_MACHINE_ID);
				}
				error = undefined;
				return state(paired);
			} catch (err) {
				error = errorMessage(err);
				return state();
			}
		},
		select,
		gatewayToken: async () => activePairedMachine ? deps.pairedTransport.token() : null,
		isPairedActive: () => activePairedMachine !== null,
	};
}
