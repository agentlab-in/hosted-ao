import type { DaemonStatus } from "../shared/daemon-status";

type SetDaemonStatus = (status: DaemonStatus) => void;

/**
 * Daemon lifecycle when remote may mean "an account machine is active".
 *
 * Remote is only the registered-machine path: the active machine's status, or
 * null when this computer is the active machine (local daemon). The old
 * AO_REMOTE_URL / AO_REMOTE_TOKEN pairing hatch is gone (spec task 15).
 */
export function createRemoteDaemonLifecycle(
	// The active registered machine's status, or null when this computer is the
	// active machine.
	activeMachineStatus: () => DaemonStatus | null = () => null,
) {
	const currentStatus = (): DaemonStatus | null => activeMachineStatus();
	const applyRemoteStatus = (setStatus: SetDaemonStatus): DaemonStatus | null => {
		const status = currentStatus();
		if (status) setStatus(status);
		return status;
	};

	return {
		currentStatus,
		async refresh(localRefresh: () => Promise<DaemonStatus>, setStatus: SetDaemonStatus): Promise<DaemonStatus> {
			return applyRemoteStatus(setStatus) ?? localRefresh();
		},
		async start(localStart: () => Promise<DaemonStatus>, setStatus: SetDaemonStatus): Promise<DaemonStatus> {
			return applyRemoteStatus(setStatus) ?? localStart();
		},
		stop(localStop: () => DaemonStatus, setStatus: SetDaemonStatus): DaemonStatus {
			return applyRemoteStatus(setStatus) ?? localStop();
		},
	};
}
