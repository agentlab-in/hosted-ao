import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AoMachine } from "../shared/ao-machines";
import type { DaemonStatus } from "../shared/daemon-status";
import { GATEWAY_COOKIE_NAME } from "../shared/remote-daemon";
import type { ControlPlaneTokenSource } from "./ao-control-token";
import { MachineUnavailableError, type MachineAccessToken, type MachineTokenSource } from "./ao-machine-token";
import { createMachineTransport, SIGNED_OUT_REASON, type MachineTransport } from "./machine-transport";

const NOW = 1_700_000_000_000;
const TTL_MS = 900_000;

const machine = (extra: Partial<AoMachine> = {}): AoMachine => ({
	id: "mch_1",
	name: "ao-build-01",
	baseUrl: "https://vm.example.com",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
	...extra,
});

const controlToken: ControlPlaneTokenSource = { get: async () => "control.plane.jwt", clear: () => undefined };

/**
 * A token source under the test's control: `mode` decides what the next call
 * does, so a refresh can be made to succeed with a new value, fail, or report the
 * machine gone, without a fake HTTP layer in the way.
 *
 * It caches like the real one (ao-machine-token.ts): `get()` reuses a token that
 * is still comfortably valid, `mint()` always issues a new one. That split is
 * what the transport's refresh depends on, so a fake without it would let a
 * refresh that never actually rotates the token pass.
 */
function fakeTokens(clock: { ms: number }) {
	let issued = 0;
	let cached: MachineAccessToken | null = null;
	const state = {
		mode: "ok" as "ok" | "signed-out" | "gone" | "error",
		calls: 0,
		created: [] as string[],
	};
	const issue = async (): Promise<MachineAccessToken | null> => {
		state.calls += 1;
		if (state.mode === "signed-out") return null;
		if (state.mode === "gone") throw new MachineUnavailableError();
		if (state.mode === "error") throw new Error("The control plane is unreachable.");
		issued += 1;
		cached = { token: `machine.jwt.${issued}`, expiresAt: clock.ms + TTL_MS };
		return cached;
	};
	const source: MachineTokenSource = {
		get: async () => (cached && cached.expiresAt - 60_000 > clock.ms ? cached : issue()),
		mint: issue,
		clear: () => {
			cached = null;
		},
	};
	return { state, source };
}

function harness() {
	const clock = { ms: NOW };
	const tokens = fakeTokens(clock);
	const set = vi.fn().mockResolvedValue(undefined);
	const remove = vi.fn().mockResolvedValue(undefined);
	const onStatus = vi.fn<(status: DaemonStatus | null) => void>();
	const onMachineGone = vi.fn();
	const createTokenSource = vi.fn((deps: { machineId: string }) => {
		tokens.state.created.push(deps.machineId);
		return tokens.source;
	});
	const transport: MachineTransport = createMachineTransport({
		cookies: { set, remove },
		controlPlaneUrl: "https://ao.agentlab.in",
		controlToken,
		onStatus,
		onMachineGone,
		now: () => clock.ms,
		// Tracks the same fake clock by default, so every existing test (which
		// advances time only via `clock.ms` + fake timers) sees wall and
		// monotonic move together, exactly as they do outside of a clock-skew
		// scenario.
		monotonicNow: () => clock.ms,
		createTokenSource,
	});
	return { clock, tokens, set, remove, onStatus, onMachineGone, createTokenSource, transport };
}

/** Let the transport's own awaits settle without advancing the fake clock. */
const settle = async (): Promise<void> => {
	await vi.advanceTimersByTimeAsync(0);
};

beforeEach(() => {
	vi.useFakeTimers();
});

afterEach(() => {
	vi.useRealTimers();
});

describe("connecting to a machine", () => {
	it("installs ao_gw_token as a Secure HttpOnly SameSite=None host-only cookie on the gateway origin", async () => {
		const { transport, set } = harness();

		transport.setMachine(machine());
		await settle();

		expect(set).toHaveBeenCalledTimes(1);
		expect(set.mock.calls[0][0]).toEqual({
			url: "https://vm.example.com",
			name: GATEWAY_COOKIE_NAME,
			value: "machine.jwt.1",
			path: "/",
			secure: true,
			httpOnly: true,
			// SameSite=None: app://renderer reaching https://vm.example.com is a
			// cross-site context, and under the browser default the cookie is not
			// attached to the /mux handshake or the EventSource request at all.
			sameSite: "no_restriction",
		});
		// Host-only. A Domain would offer the credential to every sibling host
		// under the same registrable domain.
		expect(set.mock.calls[0][0]).not.toHaveProperty("domain");
	});

	// Ordering is the whole safety property: the ready status is what sets the
	// renderer's API base URL, and it opens the SSE stream and the terminal mux
	// immediately. Publishing it before the cookie exists makes both 401.
	it("reports connecting first and only reports ready after the cookie is installed", async () => {
		const { transport, onStatus, set } = harness();

		transport.setMachine(machine());
		expect(onStatus.mock.calls).toEqual([[{ state: "starting", message: "Connecting to ao-build-01…" }]]);
		expect(set).not.toHaveBeenCalled();

		await settle();

		expect(onStatus).toHaveBeenLastCalledWith({
			state: "ready",
			baseUrl: "https://vm.example.com",
			message: "Connected to ao-build-01",
		});
		expect(set).toHaveBeenCalledTimes(1);
		expect(set.mock.invocationCallOrder[0]).toBeLessThan(onStatus.mock.invocationCallOrder[1]);
	});

	it("never puts the token in a status", async () => {
		const { transport, onStatus } = harness();

		transport.setMachine(machine());
		await settle();

		for (const [status] of onStatus.mock.calls) {
			expect(JSON.stringify(status ?? {})).not.toContain("machine.jwt");
		}
	});

	it.each(["offline", "unknown"] as const)(
		"asks for no credential while a machine is %s, and hands out no base URL",
		async (reachability) => {
			const { transport, tokens, set, onStatus } = harness();

			transport.setMachine(machine({ reachability }));
			await settle();

			expect(tokens.state.calls).toBe(0);
			expect(set).not.toHaveBeenCalled();
			expect(onStatus).toHaveBeenCalledTimes(1);
			expect(onStatus.mock.calls[0][0]?.baseUrl).toBeUndefined();
		},
	);

	it("says to sign in when this install has no account", async () => {
		const { transport, tokens, set, onStatus } = harness();
		tokens.state.mode = "signed-out";

		transport.setMachine(machine());
		await settle();

		expect(set).not.toHaveBeenCalled();
		expect(onStatus).toHaveBeenLastCalledWith({
			state: "error",
			code: "machine_auth_failed",
			message: `Could not sign in to ao-build-01. ${SIGNED_OUT_REASON}`,
		});
	});

	it("refreshes the machine list when the control plane will not mint a token for it", async () => {
		const { transport, tokens, onStatus, onMachineGone } = harness();
		tokens.state.mode = "gone";

		transport.setMachine(machine());
		await settle();

		expect(onStatus).toHaveBeenLastCalledWith(
			expect.objectContaining({ state: "error", code: "machine_auth_failed" }),
		);
		expect(onMachineGone).toHaveBeenCalledTimes(1);
	});

	// Nothing to protect on a first connection, so the failure is reported at once
	// rather than retried quietly. The quiet retry is only for a refresh, where a
	// still-valid credential is already installed.
	it("reports a first-connection failure straight away", async () => {
		const { transport, tokens, onStatus } = harness();
		tokens.state.mode = "error";

		transport.setMachine(machine());
		await settle();

		expect(onStatus).toHaveBeenLastCalledWith({
			state: "error",
			code: "machine_auth_failed",
			message: "Could not sign in to ao-build-01. The control plane is unreachable.",
		});
	});
});

describe("the silent refresh", () => {
	/**
	 * The failure nobody notices until a VM run: a refresh that emits a status
	 * re-runs the renderer's base-URL plumbing, which closes and rebuilds the
	 * EventSource. See the matching assertion in
	 * renderer/lib/event-transport.test.ts, which pins the other half: an
	 * unchanged base URL leaves the open stream alone.
	 */
	it("replaces the cookie without emitting any status, so an open SSE stream is not dropped", async () => {
		const { transport, set, onStatus, clock } = harness();

		transport.setMachine(machine());
		await settle();
		const statusesWhileConnected = onStatus.mock.calls.length;

		clock.ms += TTL_MS - 120_000;
		await vi.advanceTimersByTimeAsync(TTL_MS - 120_000);

		expect(set).toHaveBeenCalledTimes(2);
		expect(set.mock.calls[1][0]).toMatchObject({ name: GATEWAY_COOKIE_NAME, value: "machine.jwt.2" });
		// Nothing the renderer can observe changed, so nothing reconnects.
		expect(onStatus).toHaveBeenCalledTimes(statusesWhileConnected);
		// And the base URL the renderer is pointed at is still the same one.
		expect(onStatus.mock.calls[statusesWhileConnected - 1][0]).toMatchObject({
			state: "ready",
			baseUrl: "https://vm.example.com",
		});
	});

	it("keeps refreshing, so a session outlives many token lifetimes", async () => {
		const { transport, set, onStatus, clock } = harness();

		transport.setMachine(machine());
		await settle();

		for (let i = 0; i < 4; i += 1) {
			clock.ms += TTL_MS - 120_000;
			await vi.advanceTimersByTimeAsync(TTL_MS - 120_000);
		}

		expect(set).toHaveBeenCalledTimes(5);
		expect(set.mock.calls[4][0]).toMatchObject({ value: "machine.jwt.5" });
		expect(onStatus).toHaveBeenCalledTimes(2); // connecting, then ready. Nothing since.
	});

	it("schedules off the reported expiry, not a hardcoded fifteen minutes", async () => {
		const clock = { ms: NOW };
		const set = vi.fn().mockResolvedValue(undefined);
		const shortTtl: MachineTokenSource = {
			get: async () => ({ token: "machine.jwt.short", expiresAt: clock.ms + 600_000 }),
			mint: async () => ({ token: "machine.jwt.short", expiresAt: clock.ms + 600_000 }),
			clear: () => undefined,
		};
		const transport = createMachineTransport({
			cookies: { set, remove: vi.fn().mockResolvedValue(undefined) },
			controlPlaneUrl: "https://ao.agentlab.in",
			controlToken,
			onStatus: vi.fn(),
			onMachineGone: vi.fn(),
			now: () => clock.ms,
			createTokenSource: () => shortTtl,
		});

		transport.setMachine(machine());
		await settle();

		// A ten minute token refreshes at eight, not at thirteen.
		clock.ms += 480_000;
		await vi.advanceTimersByTimeAsync(480_000);
		expect(set).toHaveBeenCalledTimes(2);
	});

	// Regression: scheduleRefresh used to compute its delay as `expiresAt -
	// REFRESH_LEAD_MS - Date.now()`. acquire() awaits installCookie (an
	// Electron IPC round trip) between minting and that computation, and a
	// backward NTP correction landing in that gap inflated the delay, firing
	// the refresh later than the token's real expiry. Anchoring the delay to
	// monotonic elapsed time since mint is immune to the jump.
	it("keeps the refresh on schedule across a backward wall-clock jump during the cookie-install await", async () => {
		const clock = { ms: NOW };
		const monotonicClock = { ms: 0 };
		const tokens = fakeTokens(clock);
		const set = vi.fn(async () => {
			// Simulate an NTP backward correction landing while installCookie is
			// in flight: wall clock jumps back ten minutes, real (monotonic) time
			// does not move.
			clock.ms -= 600_000;
		});
		const transport = createMachineTransport({
			cookies: { set, remove: vi.fn().mockResolvedValue(undefined) },
			controlPlaneUrl: "https://ao.agentlab.in",
			controlToken,
			onStatus: vi.fn(),
			onMachineGone: vi.fn(),
			now: () => clock.ms,
			monotonicNow: () => monotonicClock.ms,
			createTokenSource: () => tokens.source,
		});

		transport.setMachine(machine());
		await settle();
		expect(set).toHaveBeenCalledTimes(1);

		// Advance real (monotonic) time and fake timers by exactly the correct
		// pre-jump delay (TTL - REFRESH_LEAD_MS). If the jump had leaked into the
		// schedule, this would not be enough to fire it yet.
		const correctDelay = TTL_MS - 120_000;
		monotonicClock.ms += correctDelay;
		await vi.advanceTimersByTimeAsync(correctDelay);

		expect(set).toHaveBeenCalledTimes(2);
	});

	it("stays quiet and retries while the installed token is still valid", async () => {
		const { transport, tokens, onStatus, set, clock } = harness();

		transport.setMachine(machine());
		await settle();
		const statusesWhileConnected = onStatus.mock.calls.length;

		tokens.state.mode = "error";
		clock.ms += TTL_MS - 120_000;
		await vi.advanceTimersByTimeAsync(TTL_MS - 120_000);

		// A control-plane outage does not disconnect a working session: the token in
		// the cookie is good for two more minutes and the gateway serves from its
		// stale JWKS cache.
		expect(onStatus).toHaveBeenCalledTimes(statusesWhileConnected);

		tokens.state.mode = "ok";
		clock.ms += 30_000;
		await vi.advanceTimersByTimeAsync(30_000);

		expect(set).toHaveBeenCalledTimes(2);
		expect(onStatus).toHaveBeenCalledTimes(statusesWhileConnected);
	});

	it("reports the failure once the installed token has actually lapsed", async () => {
		const { transport, tokens, onStatus, clock } = harness();

		transport.setMachine(machine());
		await settle();

		tokens.state.mode = "error";
		clock.ms += TTL_MS - 120_000;
		await vi.advanceTimersByTimeAsync(TTL_MS - 120_000);
		// Past expiry, so the retry has nothing left to protect.
		clock.ms += 150_000;
		await vi.advanceTimersByTimeAsync(30_000);

		expect(onStatus).toHaveBeenLastCalledWith({
			state: "error",
			code: "machine_auth_failed",
			message: "Could not sign in to ao-build-01. The control plane is unreachable.",
		});
	});
});

describe("switching away", () => {
	it("drops the old machine's cookie and hands the app back to the local daemon", async () => {
		const { transport, remove, onStatus } = harness();

		transport.setMachine(machine());
		await settle();
		transport.setMachine(null);
		await settle();

		expect(remove).toHaveBeenCalledWith("https://vm.example.com", GATEWAY_COOKIE_NAME);
		// Null is what makes the remote lifecycle fall through to the local daemon.
		expect(onStatus).toHaveBeenLastCalledWith(null);
		await expect(transport.token()).resolves.toBeNull();
	});

	it("stops refreshing the machine it left", async () => {
		const { transport, set, clock } = harness();

		transport.setMachine(machine());
		await settle();
		transport.setMachine(null);
		await settle();

		clock.ms += TTL_MS;
		await vi.advanceTimersByTimeAsync(TTL_MS);

		expect(set).toHaveBeenCalledTimes(1);
	});

	it("mints for the new machine and drops the old cookie when switching between machines", async () => {
		const { transport, tokens, set, remove } = harness();

		transport.setMachine(machine());
		await settle();
		transport.setMachine(machine({ id: "mch_2", name: "ao-eu-west", baseUrl: "https://eu.example.com" }));
		await settle();

		expect(remove).toHaveBeenCalledWith("https://vm.example.com", GATEWAY_COOKIE_NAME);
		expect(tokens.state.created).toEqual(["mch_1", "mch_2"]);
		expect(set.mock.calls[1][0]).toMatchObject({ url: "https://eu.example.com" });
	});

	// The hazard: a slow token exchange for machine A landing after the user
	// picked machine B would install A's cookie and publish A's base URL.
	it("drops an acquisition that was superseded while it was in flight", async () => {
		const clock = { ms: NOW };
		const set = vi.fn().mockResolvedValue(undefined);
		const onStatus = vi.fn<(status: DaemonStatus | null) => void>();
		let release: (() => void) | undefined;
		const blockUntilReleased = async (): Promise<MachineAccessToken> => {
			await new Promise<void>((resolve) => {
				release = resolve;
			});
			return { token: "machine.jwt.slow", expiresAt: clock.ms + TTL_MS };
		};
		const slow: MachineTokenSource = {
			get: blockUntilReleased,
			mint: blockUntilReleased,
			clear: () => undefined,
		};
		const transport = createMachineTransport({
			cookies: { set, remove: vi.fn().mockResolvedValue(undefined) },
			controlPlaneUrl: "https://ao.agentlab.in",
			controlToken,
			onStatus,
			onMachineGone: vi.fn(),
			now: () => clock.ms,
			createTokenSource: () => slow,
		});

		transport.setMachine(machine());
		await settle();
		transport.setMachine(null);
		release?.();
		await settle();

		expect(set).not.toHaveBeenCalled();
		expect(onStatus).toHaveBeenLastCalledWith(null);
	});
});

describe("the REST bearer", () => {
	it("hands out the current token for the active machine", async () => {
		const { transport } = harness();

		transport.setMachine(machine());
		await settle();

		await expect(transport.token()).resolves.toBe("machine.jwt.1");
	});

	it("is null while this computer is the active machine", async () => {
		const { transport } = harness();

		await expect(transport.token()).resolves.toBeNull();
	});

	it("is null rather than a rejection when the token cannot be obtained", async () => {
		const { transport, tokens, clock } = harness();
		transport.setMachine(machine());
		await settle();

		tokens.state.mode = "error";
		clock.ms += TTL_MS; // the cached token is spent, so this has to mint and fails
		// A rejection here would surface as an unhandled error on the IPC channel;
		// the REST call gets a 401 from the gateway instead, which the renderer
		// already renders through the machine's status.
		await expect(transport.token()).resolves.toBeNull();
	});
});
