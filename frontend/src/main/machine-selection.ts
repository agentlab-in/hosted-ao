import type { AoMachine, AoMachinesState } from "../shared/ao-machines";
import type { MachineTransport } from "./machine-transport";
import type { PairedMachineTransport } from "./paired-machine-transport";

/**
 * The one entry point for making a machine active: local, hosted, or paired
 * (docs/plans/2026-08-16-pair-by-ip-headless-boxes.md, task 9).
 *
 * `ao-machines.ts`'s own `select()` only ever searches the control-plane-listed
 * `remote` array, so a paired machine id would be rejected as "no longer in
 * this account's list". This module resolves a paired id first and drives the
 * passcode-credentialed transport instead, without touching `ao-machines.ts`
 * at all: local and hosted selection keep calling straight through to it,
 * unchanged.
 *
 * Two transports, two credential models, but only one can be allowed to write
 * the app's daemon status at a time. `isPairedActive()` is that arbiter: it is
 * true from the moment a paired `select()` succeeds until the next local or
 * hosted `select()`, and main.ts's own onStatus callbacks for the two
 * transports each check it (with opposite polarity) before writing
 * `activeMachineStatus`. That guard is what lets `select()` below park the
 * transport being left, by calling its own `setMachine(null, ...)`, without
 * that teardown call's status push racing the one just activated: whichever
 * transport the flag says is NOT current has its write dropped, so flipping
 * the flag before touching either transport makes the switch atomic from the
 * status sink's point of view. See main.ts's two `onStatus` wirings.
 */

/** The slice of `ao-machines.ts`'s controller this module drives. */
export type MachineSelectionAoMachines = {
	getState: () => AoMachinesState;
	refresh: () => Promise<AoMachinesState>;
	select: (machineId: string) => Promise<AoMachinesState>;
};

/** The slice of `paired-machines.ts`'s controller this module reads. */
export type MachineSelectionPairedMachines = {
	list: () => AoMachine[];
	getPinnedFingerprint: (id: string) => string | null;
	getPasscode: (id: string) => Promise<string | null>;
};

export type MachineSelectionDeps = {
	aoMachines: MachineSelectionAoMachines;
	pairedMachines: MachineSelectionPairedMachines;
	pairedTransport: PairedMachineTransport;
	/**
	 * The hosted transport, or null when no control-plane credential can exist
	 * (main.ts's `machineTransport()` already reports that as its own state).
	 * Read lazily via a getter, exactly like main.ts's own
	 * `machineTransportInstance` access, so parking one never forces a hosted
	 * transport into existence just to immediately null it out.
	 */
	getHostedTransport: () => MachineTransport | null;
};

export type MachineSelection = {
	/** Current known state, paired-overlaid. No network: same contract as ao-machines.ts's own getState(). */
	getState: () => AoMachinesState;
	/** Re-list and re-probe the control-plane machines, paired-overlaid. */
	refresh: () => Promise<AoMachinesState>;
	/** Make one machine (local, hosted, or paired) active. */
	select: (machineId: string) => Promise<AoMachinesState>;
	/** The gateway bearer for a REST call to whichever transport is active. */
	gatewayToken: (forceRefresh?: boolean) => Promise<string | null>;
	/** Whether a paired machine currently owns the app's daemon status. */
	isPairedActive: () => boolean;
};

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function createMachineSelection(deps: MachineSelectionDeps): MachineSelection {
	let activePairedMachine: AoMachine | null = null;

	function overlay(state: AoMachinesState): AoMachinesState {
		return activePairedMachine ? { ...state, activeMachineId: activePairedMachine.id } : state;
	}

	async function selectPaired(paired: AoMachine): Promise<AoMachinesState> {
		// A machine whose fingerprint is not pinned is not connectable: selection
		// must not be a way around the comparison step. `add()` never stores a
		// record without an accepted fingerprint, and the certificate pin would
		// refuse the TLS handshake anyway (paired-machine-cert.ts), but this must
		// fail closed on its own rather than rely only on that second layer.
		const pinned = deps.pairedMachines.getPinnedFingerprint(paired.id);
		if (!pinned) {
			return overlay({
				...deps.aoMachines.getState(),
				error: `${paired.name} has no accepted fingerprint. Re-pair it before connecting.`,
			});
		}

		let passcode: string | null;
		try {
			passcode = await deps.pairedMachines.getPasscode(paired.id);
		} catch (err) {
			// The passcode itself is never in this message; getPasscode's own
			// failure text (a decrypt error, no OS credential store) already avoids it.
			return overlay({ ...deps.aoMachines.getState(), error: errorMessage(err) });
		}
		if (!passcode) {
			return overlay({ ...deps.aoMachines.getState(), error: `${paired.name} has no stored passcode. Re-pair it.` });
		}

		// Flip the flag before touching either transport (see the module doc
		// comment): this is what makes the hosted transport's own onStatus guard
		// drop the write from parking it next, instead of that write racing the
		// paired status about to be published.
		activePairedMachine = paired;
		deps.getHostedTransport()?.setMachine(null);
		deps.pairedTransport.setMachine(paired, passcode);

		return overlay(deps.aoMachines.getState());
	}

	async function select(machineId: string): Promise<AoMachinesState> {
		const paired = deps.pairedMachines.list().find((machine) => machine.id === machineId);
		if (paired) return selectPaired(paired);

		// Local or a hosted machine. Clear the flag first: the paired transport's
		// own onStatus is guarded on it, so this teardown call's null status push
		// is dropped rather than landing after the hosted/local select below
		// publishes the real one.
		activePairedMachine = null;
		deps.pairedTransport.setMachine(null, null);

		// Never touched for a paired id (selectPaired above returns before this
		// point is reached), so a hosted machine keeps using its JWT credential
		// path exactly as ao-machines.ts already implements it.
		return overlay(await deps.aoMachines.select(machineId));
	}

	return {
		getState: () => overlay(deps.aoMachines.getState()),
		refresh: async () => overlay(await deps.aoMachines.refresh()),
		select,
		gatewayToken: async (forceRefresh) => {
			if (activePairedMachine) return deps.pairedTransport.token();
			return (await deps.getHostedTransport()?.token(forceRefresh)) ?? null;
		},
		isPairedActive: () => activePairedMachine !== null,
	};
}
