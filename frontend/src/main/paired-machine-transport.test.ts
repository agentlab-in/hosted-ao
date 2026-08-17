import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { AoMachine } from "../shared/ao-machines";
import type { DaemonStatus } from "../shared/daemon-status";
import { GATEWAY_COOKIE_NAME } from "../shared/remote-daemon";
import { createPairedMachineTransport } from "./paired-machine-transport";

const PASSCODE = "sup3rSecr3t";

const machine = (extra: Partial<AoMachine> = {}): AoMachine => ({
	id: "box_1",
	name: "Pi in the closet",
	baseUrl: "https://192.168.1.5:8443",
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
	...extra,
});

function harness() {
	const set = vi.fn().mockResolvedValue(undefined);
	const remove = vi.fn().mockResolvedValue(undefined);
	const onStatus = vi.fn<(status: DaemonStatus | null) => void>();
	const transport = createPairedMachineTransport({ cookies: { set, remove }, onStatus });
	return { set, remove, onStatus, transport };
}

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

const settle = async (): Promise<void> => {
	await vi.advanceTimersByTimeAsync(0);
};

test("sends the passcode as a bearer credential via token(), never a control-plane call", async () => {
	const { transport, onStatus } = harness();
	transport.setMachine(machine(), PASSCODE);
	await settle();

	expect(await transport.token()).toBe(PASSCODE);
	// Ready only after the cookie carrying the same passcode is installed.
	expect(onStatus).toHaveBeenLastCalledWith(
		expect.objectContaining({ state: "ready", baseUrl: "https://192.168.1.5:8443" }),
	);
});

test("installs the gateway cookie with the passcode, the same shape /mux and SSE read", async () => {
	const { transport, set } = harness();
	transport.setMachine(machine(), PASSCODE);
	await settle();

	expect(set).toHaveBeenCalledExactlyOnceWith(
		expect.objectContaining({
			url: "https://192.168.1.5:8443",
			name: GATEWAY_COOKIE_NAME,
			value: PASSCODE,
			secure: true,
			httpOnly: true,
			sameSite: "no_restriction",
		}),
	);
});

test("publishes connecting, then ready, in that order", async () => {
	const { transport, onStatus } = harness();
	transport.setMachine(machine(), PASSCODE);
	// Before the cookie install (a microtask) resolves, only "connecting" is out.
	expect(onStatus).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ state: "starting" }));

	await settle();
	expect(onStatus).toHaveBeenLastCalledWith(expect.objectContaining({ state: "ready" }));
});

test("this transport has no control-plane dependency of any kind to call", () => {
	// Structural guarantee, not just a runtime assertion: createPairedMachineTransport's
	// deps type carries no fetch implementation, no control-plane URL, and no
	// token source -- there is nothing here capable of reaching the control plane.
	const deps = harness();
	expect(Object.keys(deps.transport)).toEqual(["setMachine", "token"]);
});

test("no reachability check means no acquisition: reachability !== online never installs a cookie", async () => {
	const { transport, set, onStatus } = harness();
	transport.setMachine(machine({ reachability: "offline" }), PASSCODE);
	await settle();

	expect(set).not.toHaveBeenCalled();
	expect(onStatus).toHaveBeenLastCalledWith(expect.objectContaining({ state: "error", code: "daemon_unreachable" }));
});

test("no passcode: an auth-failed status, and no cookie install attempted", async () => {
	const { transport, set, onStatus } = harness();
	transport.setMachine(machine(), null);
	await settle();

	expect(set).not.toHaveBeenCalled();
	expect(onStatus).toHaveBeenCalledExactlyOnceWith(
		expect.objectContaining({ state: "error", code: "machine_auth_failed" }),
	);
	expect(await transport.token()).toBeNull();
});

test("setMachine(null) clears the token, drops the cookie, and publishes null", async () => {
	const { transport, set, remove, onStatus } = harness();
	transport.setMachine(machine(), PASSCODE);
	await settle();

	transport.setMachine(null, null);
	await settle();

	expect(await transport.token()).toBeNull();
	expect(remove).toHaveBeenCalledExactlyOnceWith("https://192.168.1.5:8443", GATEWAY_COOKIE_NAME);
	expect(onStatus).toHaveBeenLastCalledWith(null);
	expect(set).toHaveBeenCalledTimes(1); // only the earlier connect, never re-installed
});

test("switching to a different machine's baseUrl drops the old cookie", async () => {
	const { transport, remove } = harness();
	transport.setMachine(machine(), PASSCODE);
	await settle();

	transport.setMachine(machine({ id: "box_2", baseUrl: "https://192.168.1.9:8443" }), "other-code");
	await settle();

	expect(remove).toHaveBeenCalledExactlyOnceWith("https://192.168.1.5:8443", GATEWAY_COOKIE_NAME);
});

test("a superseded connect never publishes its status: the newest setMachine wins", async () => {
	const { transport, onStatus, set } = harness();
	// Make the first cookie install hang so the second setMachine can race it.
	set.mockReturnValueOnce(new Promise(() => undefined));
	transport.setMachine(machine(), PASSCODE);
	transport.setMachine(null, null);
	await settle();

	// The stale "ready" from the first call must never land after the null.
	expect(onStatus).toHaveBeenLastCalledWith(null);
});

test("the passcode never appears in any status the transport publishes", async () => {
	const { transport, onStatus } = harness();
	transport.setMachine(machine(), PASSCODE);
	transport.setMachine(machine(), null); // auth-failed path
	await settle();

	for (const call of onStatus.mock.calls) {
		expect(JSON.stringify(call[0])).not.toContain(PASSCODE);
	}
});
