import type { DaemonStatus } from "../shared/daemon-status";

export type TerminalThemeScheme = "light" | "dark";

type PersistTerminalThemeDeps = {
	isRemoteSelected: () => boolean;
	activeRemoteStatus: () => DaemonStatus | null;
	gatewayToken: () => Promise<string | null>;
	writeLocal: (scheme: TerminalThemeScheme) => void;
	fetchRemote: (url: string, init: RequestInit) => Promise<{ ok: boolean; status: number }>;
};

// Persist the hint on the daemon that will spawn the next PTY. A selected
// remote machine must never fall through to a local filesystem write: if its
// authenticated gateway is unavailable, retaining the daemon's previous hint
// is safer than silently updating the wrong machine.
export async function persistSelectedDaemonTerminalTheme(
	scheme: TerminalThemeScheme,
	deps: PersistTerminalThemeDeps,
): Promise<void> {
	if (!deps.isRemoteSelected()) {
		deps.writeLocal(scheme);
		return;
	}

	const status = deps.activeRemoteStatus();
	if (status?.state !== "ready" || !status.baseUrl) {
		throw new Error("selected remote daemon is not ready for terminal theme update");
	}
	const token = await deps.gatewayToken();
	if (!token) throw new Error("selected remote daemon has no gateway credential");
	const response = await deps.fetchRemote(`${status.baseUrl}/api/v1/settings/terminal-theme`, {
		method: "PATCH",
		headers: {
			Authorization: `Bearer ${token}`,
			"Content-Type": "application/json",
		},
		body: JSON.stringify({ scheme }),
	});
	if (!response.ok) throw new Error(`remote terminal theme update failed (${response.status})`);
}
