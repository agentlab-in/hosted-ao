/**
 * The peer daemon's workspaces: a read-only view of the daemon that is NOT the
 * active one, so the desktop app can list its projects and sessions alongside
 * the active machine's without re-pointing anything at it.
 *
 * "Peer" is defined relative to `activeMachineId` in `./ao-machines`: when this
 * computer is active, the peer is the account's online registered machine (if
 * any); when a registered machine is active, the peer is this computer's local
 * loopback daemon. See `main/peer-workspaces.ts` for the fetch and shaping.
 *
 * Pure shapes only, no I/O, matching the split every other shared module here
 * follows (main owns the fetch, the renderer owns the rendering).
 */

/** One session on the peer daemon. Read-only: no terminal, no SSE. */
export type PeerSession = {
	id: string;
	title: string;
	status: string;
	activity?: string;
	branch?: string;
	harness?: string;
	kind?: string;
	updatedAt?: string;
};

/** One project on the peer daemon, with its sessions. */
export type PeerProject = {
	id: string;
	name: string;
	sessions: PeerSession[];
};

export type PeerWorkspacesResult =
	| { state: "unavailable"; reason: string }
	| { state: "ok"; machineId: string; machineName: string; isRemote: boolean; projects: PeerProject[] };
