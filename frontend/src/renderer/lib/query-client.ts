import { QueryClient } from "@tanstack/react-query";
import { subscribeApiBaseUrl } from "./api-client";

export const queryClient = new QueryClient({
	defaultOptions: {
		queries: {
			staleTime: 10_000,
			refetchOnWindowFocus: false,
		},
	},
});

/**
 * Drop everything cached when the app is re-pointed at a different daemon.
 * Called once from the app root.
 *
 * Query keys carry no machine dimension, so machine A's projects and sessions
 * are cached under exactly the keys machine B reads. Without this, a switch
 * renders A's session list under B's identity, and a request already in flight
 * to A resolves afterwards and writes A's answer into the same key, which is
 * the one a user could act on. Clearing beats adding a machine dimension to
 * every key: it is one line, and it cannot be forgotten on a key added later.
 *
 * setApiBaseUrl only notifies on an actual change, so this does not fire on a
 * refresh that lands on the same daemon.
 */
export function clearQueryCacheOnMachineSwitch(): () => void {
	return subscribeApiBaseUrl(() => {
		queryClient.clear();
	});
}
