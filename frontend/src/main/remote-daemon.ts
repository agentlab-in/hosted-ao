import { REMOTE_PAIRING_COOKIE_NAME, type RemoteDaemonConfig } from "../shared/remote-daemon";
import { formatLastSeen, type AoMachine } from "../shared/ao-machines";
import type { DaemonStatus } from "../shared/daemon-status";

type CookieStore = {
	set: (details: Electron.CookiesSetDetails) => Promise<void>;
};

type SetDaemonStatus = (status: DaemonStatus) => void;

const remoteDaemonCookieFailureStatus = (): DaemonStatus => ({
	state: "error",
	message: "Could not configure remote daemon authentication.",
	code: "not_configured",
});

export async function installRemoteDaemonCookie(store: CookieStore, config: RemoteDaemonConfig): Promise<void> {
	await store.set({
		url: config.baseUrl,
		name: REMOTE_PAIRING_COOKIE_NAME,
		value: config.token,
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "no_restriction",
	});
}

export function remoteDaemonReadyStatus(config: RemoteDaemonConfig): DaemonStatus {
	return { state: "ready", baseUrl: config.baseUrl, message: "Connected to remote daemon" };
}

/**
 * The app's daemon status while a registered machine is the active one.
 *
 * An unreachable machine is an error status, not the absence of one, and that
 * is the whole point: createRemoteDaemonLifecycle only falls through to the
 * local daemon when there is no status here at all. A remote machine being
 * down therefore cannot spawn a local daemon, because the code path that
 * spawns one is never reached rather than being skipped by a condition.
 */
export function machineDaemonStatus(machine: AoMachine): DaemonStatus {
	if (machine.reachability === "offline") {
		const seen = formatLastSeen(machine.lastSeen);
		return {
			state: "error",
			code: "daemon_unreachable",
			message: seen
				? `${machine.name} is not reachable. Last seen ${seen}.`
				: `${machine.name} is not reachable, and has never connected to AO.`,
		};
	}
	if (machine.reachability === "unknown") {
		return { state: "starting", message: `Connecting to ${machine.name}…` };
	}
	return { state: "ready", baseUrl: machine.baseUrl, message: `Connected to ${machine.name}` };
}

export function createRemoteDaemonLifecycle(
	config: RemoteDaemonConfig | null,
	initialFailureStatus: DaemonStatus | null = null,
	// The active registered machine's status, or null when this computer is the
	// active machine. AO_REMOTE_URL is checked first so the env pairing hatch
	// keeps behaving exactly as it did before machines existed.
	activeMachineStatus: () => DaemonStatus | null = () => null,
) {
	let failureStatus = initialFailureStatus;

	const currentStatus = (): DaemonStatus | null =>
		failureStatus ?? (config ? remoteDaemonReadyStatus(config) : activeMachineStatus());
	const applyRemoteStatus = (setStatus: SetDaemonStatus): DaemonStatus | null => {
		const status = currentStatus();
		if (status) setStatus(status);
		return status;
	};

	return {
		currentStatus,
		async installCookie(store: CookieStore): Promise<DaemonStatus | null> {
			if (!config || failureStatus) return currentStatus();
			try {
				await installRemoteDaemonCookie(store, config);
			} catch {
				failureStatus = remoteDaemonCookieFailureStatus();
			}
			return currentStatus();
		},
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
