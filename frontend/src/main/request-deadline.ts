/**
 * Deadlines for control-plane work, so a stalled request cannot wedge the app.
 *
 * Every call from the desktop to the control plane used to be unbounded. That
 * is survivable for a request that fails, because a rejection propagates and
 * the UI shows an error. It is not survivable for a request that never settles
 * at all: an orphaned socket (a network that changed under an in-flight fetch,
 * or an address that blackholes rather than refusing) leaves a promise pending
 * forever, and everything awaiting it waits forever too.
 *
 * That is not hypothetical. The control plane's public IP changed while the app
 * was running; the old address dropped packets instead of refusing them, and
 * the machine list spun indefinitely with no request in flight and no error to
 * show, recoverable only by restarting the app.
 *
 * The rule these two helpers enforce: **every promise the token sources and the
 * machine list await must settle.** `fetchWithDeadline` bounds the network call
 * and reports a timeout as a timeout. `withDeadline` bounds a whole operation,
 * including the non-network parts, which matters wherever a pending promise is
 * cached and handed to later callers (see the in-flight dedupe in
 * ao-control-token.ts: it clears in a `.finally`, which a promise that never
 * settles never reaches, so one stranded exchange poisons every later one).
 */

/**
 * Budget for a single control-plane request. Generous: this is a correctness
 * backstop against a hung socket, not a latency target. A healthy control plane
 * answers these in tens of milliseconds.
 */
export const CONTROL_PLANE_TIMEOUT_MS = 15_000;

function timeoutMessage(what: string, timeoutMs: number): string {
	return `${what} timed out after ${Math.round(timeoutMs / 1000)}s. The control plane may be unreachable.`;
}

/**
 * Run a fetch with an abort deadline, reporting an abort as a timeout rather
 * than the bare `AbortError` the caller would otherwise have to interpret.
 *
 * `what` is a human phrase naming the operation, for example "Listing
 * machines". It reaches the UI, so it must never contain a token or a URL with
 * credentials in it.
 */
export async function fetchWithDeadline(
	fetchImpl: typeof fetch,
	url: string,
	init: RequestInit,
	timeoutMs: number,
	what: string,
): Promise<Response> {
	const controller = new AbortController();
	const call = fetchImpl(url, { ...init, signal: controller.signal });
	// The deadline is a race, not only an abort. Aborting alone is not enough:
	// it relies on the fetch implementation honouring the signal, and one that
	// does not would still hang the caller forever, which is the exact failure
	// this module exists to make impossible. The race guarantees settlement; the
	// abort is what actually releases the socket.
	//
	// The no-op catch marks a late rejection as handled. Once the race has been
	// decided by the deadline, the abandoned call rejecting later would
	// otherwise surface as an unhandled rejection.
	void call.catch(() => {});
	try {
		return await withDeadline(call, timeoutMs, what);
	} catch (err) {
		// Only on failure. Aborting a successful response would tear down the body
		// stream before the caller has read it.
		controller.abort();
		throw err;
	}
}

/**
 * Bound a whole operation, so the returned promise is guaranteed to settle.
 *
 * Use this where a pending promise is cached and shared with later callers. A
 * bounded fetch is not sufficient there: anything else in the operation that
 * hangs (a keychain read, a filesystem write on a stalled mount) would still
 * strand the cached promise permanently.
 *
 * The losing side of the race is not cancelled, because there is nothing
 * generic to cancel; it is abandoned. Callers that also need the underlying
 * work stopped must pass their own abort signal into it.
 */
export async function withDeadline<T>(work: Promise<T>, timeoutMs: number, what: string): Promise<T> {
	let timer: ReturnType<typeof setTimeout> | undefined;
	const deadline = new Promise<never>((_resolve, reject) => {
		timer = setTimeout(() => reject(new Error(timeoutMessage(what, timeoutMs))), timeoutMs);
	});
	try {
		return await Promise.race([work, deadline]);
	} finally {
		clearTimeout(timer);
	}
}
