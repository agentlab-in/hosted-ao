import { REMOTE_PAIRING_COOKIE_NAME, type RemoteDaemonConfig } from "../shared/remote-daemon";
import type { DaemonStatus } from "../shared/daemon-status";

type CookieStore = {
	set: (details: Electron.CookiesSetDetails) => Promise<void>;
};

export async function installRemoteDaemonCookie(store: CookieStore, config: RemoteDaemonConfig): Promise<void> {
	await store.set({
		url: config.baseUrl,
		name: REMOTE_PAIRING_COOKIE_NAME,
		value: config.token,
		domain: new URL(config.baseUrl).hostname,
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "no_restriction",
	});
}

export function remoteDaemonReadyStatus(config: RemoteDaemonConfig): DaemonStatus {
	return { state: "ready", baseUrl: config.baseUrl, message: "Connected to remote daemon" };
}
