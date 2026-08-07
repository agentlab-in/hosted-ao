// DaemonStatus is the supervisor → renderer handshake payload, shared by the
// Electron main process (which derives it) and the preload bridge (which types
// the IPC surface). The renderer picks it up through the preload's AoBridge type.
// Machine-readable failure classification for telemetry. `message` is
// human-facing and may contain local paths; `code` is what gets reported.
// Statuses without a code (normal ready, user-initiated stop) are not failures.
export type DaemonFailureCode =
	| "not_configured"
	| "daemon_unreachable"
	| "binary_missing"
	| "spawn_failed"
	| "exited"
	| "port_unconfirmed"
	| "not_ready"
	| "identity_mismatch"
	| "datadir_unwritable"
	// A registered machine is up, but no machine-audience access token could be
	// obtained for it: this install is signed out, the control plane refused the
	// sign-in, or the account no longer has that machine. See
	// main/machine-transport.ts.
	| "machine_auth_failed";

export type DaemonStatus = {
	state: "starting" | "ready" | "stopped" | "error";
	port?: number;
	pid?: number;
	executablePath?: string;
	workingDirectory?: string;
	message?: string;
	// Recent daemon stdout/stderr retained by the Electron supervisor for local
	// troubleshooting. It is never sent to telemetry.
	details?: string;
	code?: DaemonFailureCode;
	exitCode?: number | null;
	signal?: string | null;
	// Non-secret HTTPS origin used only for an externally hosted daemon.
	baseUrl?: string;
};
