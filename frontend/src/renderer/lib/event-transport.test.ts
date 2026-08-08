import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const {
	onStatusMock,
	removeStatusMock,
	getApiBaseUrlMock,
	hasTrustedApiBaseUrlMock,
	subscribeApiBaseUrlMock,
	unsubscribeBaseUrlMock,
} = vi.hoisted(() => ({
	onStatusMock: vi.fn(),
	removeStatusMock: vi.fn(),
	getApiBaseUrlMock: vi.fn(() => "http://127.0.0.1:3001"),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
	subscribeApiBaseUrlMock: vi.fn(),
	unsubscribeBaseUrlMock: vi.fn(),
}));

vi.mock("./bridge", () => ({
	aoBridge: {
		daemon: { onStatus: onStatusMock },
	},
}));

vi.mock("./api-client", () => ({
	getApiBaseUrl: getApiBaseUrlMock,
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	subscribeApiBaseUrl: subscribeApiBaseUrlMock,
}));

import { createEventTransport } from "./event-transport";
import { getEventsConnectionState, setEventsConnectionState } from "./events-connection";

class EventSourceStub {
	static instances: EventSourceStub[] = [];
	url: string;
	closed = false;
	readyState = 0; // CONNECTING
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onmessage: (() => void) | null = null;
	listeners: string[] = [];
	handlers = new Map<string, (event: Event) => void>();
	constructor(url: string, readonly options?: EventSourceInit) {
		this.url = url;
		EventSourceStub.instances.push(this);
	}
	addEventListener(type: string, listener: (event: Event) => void) {
		this.listeners.push(type);
		this.handlers.set(type, listener);
	}
	emit(type: string, data: string) {
		this.handlers.get(type)?.({ data } as unknown as Event);
	}
	close() {
		this.closed = true;
		this.readyState = 2; // CLOSED
	}
}

function fakeQueryClient() {
	return { invalidateQueries: vi.fn() } as unknown as Parameters<typeof createEventTransport>[0];
}

beforeEach(() => {
	EventSourceStub.instances = [];
	onStatusMock.mockReset().mockReturnValue(removeStatusMock);
	removeStatusMock.mockReset();
	getApiBaseUrlMock.mockReset().mockReturnValue("http://127.0.0.1:3001");
	hasTrustedApiBaseUrlMock.mockReset().mockReturnValue(true);
	subscribeApiBaseUrlMock.mockReset().mockReturnValue(unsubscribeBaseUrlMock);
	unsubscribeBaseUrlMock.mockReset();
	setEventsConnectionState("idle");
	(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub;
});

afterEach(() => {
	delete (globalThis as unknown as { EventSource?: unknown }).EventSource;
});

describe("createEventTransport", () => {
	it("opens a single SSE connection to the current base URL on connect", () => {
		createEventTransport(fakeQueryClient()).connect();

		expect(EventSourceStub.instances).toHaveLength(1);
		expect(EventSourceStub.instances[0].url).toBe("http://127.0.0.1:3001/api/v1/events");
		expect(EventSourceStub.instances[0].options).toEqual({ withCredentials: false });
		// All CDC event types plus onmessage are wired up.
		expect(EventSourceStub.instances[0].listeners).toContain("session_updated");
		expect(EventSourceStub.instances[0].onmessage).toBeTypeOf("function");
	});

	it("opens the remote CDC stream with credentials", () => {
		getApiBaseUrlMock.mockReturnValue("https://api.ao.agentlab.in");

		createEventTransport(fakeQueryClient()).connect();

		expect(EventSourceStub.instances[0]).toMatchObject({
			url: "https://api.ao.agentlab.in/api/v1/events",
			options: { withCredentials: true },
		});
	});

	it("does not reconnect when a daemon status keeps the same base URL", () => {
		createEventTransport(fakeQueryClient()).connect();
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		onStatusHandler();

		expect(EventSourceStub.instances).toHaveLength(1);
	});

	// The other half of the silent refresh, whose main-process half is pinned in
	// main/machine-transport.test.ts: the refresh replaces the ao_gw_token cookie
	// and emits no status at all, so nothing here re-points and the open stream is
	// left running. A refresh that visibly reconnected every fifteen minutes would
	// not be silent, and that is the failure nobody notices until a VM run.
	it("a token refresh does not drop the open SSE stream", () => {
		getApiBaseUrlMock.mockReturnValue("https://vm.example.com");
		createEventTransport(fakeQueryClient()).connect();
		const stream = EventSourceStub.instances[0];
		stream.readyState = 1; // OPEN
		stream.onopen?.();

		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;
		const onBaseUrlChange = subscribeApiBaseUrlMock.mock.calls[0][0] as () => void;
		// Everything a refresh could plausibly wake, with the base URL unchanged
		// because the machine did not move.
		onBaseUrlChange();
		onStatusHandler();

		expect(EventSourceStub.instances).toHaveLength(1);
		expect(stream.closed).toBe(false);
		expect(getEventsConnectionState()).toBe("connected");
	});

	it("closes the old connection and reconnects when the base URL changes", () => {
		createEventTransport(fakeQueryClient()).connect();
		const first = EventSourceStub.instances[0];
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:3099");
		onStatusHandler();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(2);
		expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:3099/api/v1/events");
	});

	it("closes the source and skips reconnecting when the base URL is untrusted", () => {
		createEventTransport(fakeQueryClient()).connect();
		const first = EventSourceStub.instances[0];
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		hasTrustedApiBaseUrlMock.mockReturnValue(false);
		onStatusHandler();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(1);
		expect(getEventsConnectionState()).toBe("disconnected");
	});

	it("debounces workspace and SCM summary invalidation after a status change", () => {
		vi.useFakeTimers();
		try {
			const queryClient = fakeQueryClient();
			createEventTransport(queryClient).connect();
			const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

			onStatusHandler();
			expect(queryClient.invalidateQueries).not.toHaveBeenCalled();
			vi.advanceTimersByTime(200);
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-scm-summary"] });
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-usage"] });
		} finally {
			vi.useRealTimers();
		}
	});

	it("invalidates only the named conversation for conversation CDC", () => {
		vi.useFakeTimers();
		try {
			const queryClient = fakeQueryClient();
			createEventTransport(queryClient).connect();
			EventSourceStub.instances[0].emit(
				"session_updated",
				JSON.stringify({
					seq: 42,
					projectId: "proj-1",
					sessionId: "chat-1",
					type: "session_updated",
					payload: {
						id: "chat-1",
						sessionId: "chat-1",
						conversationId: "conv-1",
						activity: "active",
						isTerminated: false,
					},
					createdAt: "2026-08-04T15:15:14Z",
				}),
			);

			vi.advanceTimersByTime(200);
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({
				queryKey: ["conversation", "chat-1"],
			});
			expect(queryClient.invalidateQueries).not.toHaveBeenCalledWith({ queryKey: ["workspaces"] });
			expect(queryClient.invalidateQueries).not.toHaveBeenCalledWith({
				queryKey: ["session-scm-summary"],
			});
		} finally {
			vi.useRealTimers();
		}
	});

	it("tears down the source and the daemon listener on disconnect", () => {
		const disconnect = createEventTransport(fakeQueryClient()).connect();

		disconnect();

		expect(EventSourceStub.instances[0].closed).toBe(true);
		expect(removeStatusMock).toHaveBeenCalledTimes(1);
	});

	it("is a no-op when EventSource is unavailable", () => {
		delete (globalThis as unknown as { EventSource?: unknown }).EventSource;

		expect(() => createEventTransport(fakeQueryClient()).connect()).not.toThrow();
		expect(EventSourceStub.instances).toHaveLength(0);
	});

	it("marks the stream connected on open and disconnected on error", () => {
		createEventTransport(fakeQueryClient()).connect();
		const source = EventSourceStub.instances[0];

		source.readyState = 1; // OPEN
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");

		source.readyState = 0; // CONNECTING — browser is auto-retrying
		source.onerror?.();
		expect(getEventsConnectionState()).toBe("disconnected");

		source.readyState = 1;
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");
	});

	it("rebuilds a source the browser abandoned after the retry delay", () => {
		vi.useFakeTimers();
		try {
			createEventTransport(fakeQueryClient()).connect();
			const source = EventSourceStub.instances[0];

			source.readyState = 2; // CLOSED — EventSource gave up for good
			source.onerror?.();

			expect(EventSourceStub.instances).toHaveLength(1);
			vi.advanceTimersByTime(5_000);
			expect(EventSourceStub.instances).toHaveLength(2);
			expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:3001/api/v1/events");
		} finally {
			vi.useRealTimers();
		}
	});

	// Regression: SSE reconnect was a fixed 5s retry with no backoff and no
	// ceiling, unlike main/supervisor-link.ts and main/browser-runtime-link.ts,
	// which both use bounded exponential backoff — a down gateway got a fresh
	// handshake every 5s forever.
	it("backs off exponentially on repeated abandonment, capped at 30s, and resets after a successful reconnect", () => {
		vi.useFakeTimers();
		try {
			createEventTransport(fakeQueryClient()).connect();

			const abandon = (index: number) => {
				const source = EventSourceStub.instances[index];
				source.readyState = 2; // CLOSED — EventSource gave up for good
				source.onerror?.();
			};

			// First retry: the same prompt 5s as before this fix, for the common
			// transient case.
			abandon(0);
			vi.advanceTimersByTime(4_999);
			expect(EventSourceStub.instances).toHaveLength(1);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(2);

			// Second retry: doubled to 10s.
			abandon(1);
			vi.advanceTimersByTime(9_999);
			expect(EventSourceStub.instances).toHaveLength(2);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(3);

			// Third retry: doubled to 20s.
			abandon(2);
			vi.advanceTimersByTime(19_999);
			expect(EventSourceStub.instances).toHaveLength(3);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(4);

			// Fourth retry would double to 40s, capped at 30s.
			abandon(3);
			vi.advanceTimersByTime(29_999);
			expect(EventSourceStub.instances).toHaveLength(4);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(5);

			// A successful open resets the backoff back to the initial 5s delay.
			const reconnected = EventSourceStub.instances[4];
			reconnected.readyState = 1; // OPEN
			reconnected.onopen?.();
			abandon(4);
			vi.advanceTimersByTime(4_999);
			expect(EventSourceStub.instances).toHaveLength(5);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(6);
		} finally {
			vi.useRealTimers();
		}
	});

	it("reconnects when the API base URL changes out-of-band", () => {
		createEventTransport(fakeQueryClient()).connect();
		expect(subscribeApiBaseUrlMock).toHaveBeenCalledTimes(1);
		const onBaseUrlChange = subscribeApiBaseUrlMock.mock.calls[0][0] as () => void;
		const first = EventSourceStub.instances[0];

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:4555");
		onBaseUrlChange();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(2);
		expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:4555/api/v1/events");
	});

	it("resets the connection state and unsubscribes on disconnect", () => {
		const disconnect = createEventTransport(fakeQueryClient()).connect();
		const source = EventSourceStub.instances[0];
		source.readyState = 1;
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");

		disconnect();

		expect(getEventsConnectionState()).toBe("idle");
		expect(unsubscribeBaseUrlMock).toHaveBeenCalledTimes(1);
	});
});
