import { useQuery } from "@tanstack/react-query";
import { aoBridge } from "../lib/bridge";
import type { PeerWorkspacesResult } from "../../shared/peer-workspaces";

export const peerWorkspacesQueryKey = ["peer-workspaces"] as const;

async function fetchPeerWorkspaces(): Promise<PeerWorkspacesResult> {
	return aoBridge.machines.peerWorkspaces();
}

/**
 * Mirror of useWorkspaceQuery for the peer (non-active) daemon: "peer" is the
 * cloud machine when local is active, and local when a cloud machine is
 * active. Own query key, same polling interval, no SSE (peer sessions are
 * read-only in this UI). Only enabled while the Cloud toggle is on, so
 * turning it off issues no peer fetch.
 */
export function usePeerWorkspacesQuery(enabled: boolean) {
	return useQuery({
		queryKey: peerWorkspacesQueryKey,
		queryFn: fetchPeerWorkspaces,
		retry: 1,
		refetchInterval: 15_000,
		enabled,
	});
}
