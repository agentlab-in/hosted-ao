import { afterEach, expect, test } from "vitest";
import { setApiBaseUrl } from "./api-client";
import { clearQueryCacheOnMachineSwitch, queryClient } from "./query-client";

let stop: (() => void) | null = null;
afterEach(() => {
	stop?.();
	stop = null;
	queryClient.clear();
});

/**
 * Switching machines re-points every request at a different daemon, and query
 * keys carry no machine dimension: machine A's sessions are cached under
 * exactly the keys machine B reads. The hazard is not a stale render, it is a
 * user acting on the wrong machine's session list.
 */
test("a change of daemon empties the cache, so no machine's data outlives it", () => {
	stop = clearQueryCacheOnMachineSwitch();
	setApiBaseUrl("https://a.example.com");
	queryClient.setQueryData(["sessions"], [{ id: "from-machine-a" }]);

	setApiBaseUrl("https://b.example.com");

	expect(queryClient.getQueryData(["sessions"])).toBeUndefined();
});

test("a refresh that lands on the same daemon keeps the cache", () => {
	stop = clearQueryCacheOnMachineSwitch();
	setApiBaseUrl("https://a.example.com");
	queryClient.setQueryData(["sessions"], [{ id: "from-machine-a" }]);

	setApiBaseUrl("https://a.example.com/");

	expect(queryClient.getQueryData(["sessions"])).toEqual([{ id: "from-machine-a" }]);
});
