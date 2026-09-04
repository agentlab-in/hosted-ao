import { describe, expect, test, vi } from "vitest";
import { isPrivateHost, orderedHints, racePairAddresses, type ProbeFn } from "./pair-race";

const WANT = "match-fp";

describe("isPrivateHost", () => {
	test("recognizes RFC 1918 and loopback-adjacent private hosts", () => {
		expect(isPrivateHost("192.168.1.5")).toBe(true);
		expect(isPrivateHost("192.168.255.254")).toBe(true);
		expect(isPrivateHost("10.0.0.9")).toBe(true);
		expect(isPrivateHost("10.255.255.255")).toBe(true);
		expect(isPrivateHost("172.16.0.1")).toBe(true);
		expect(isPrivateHost("172.31.255.255")).toBe(true);
		expect(isPrivateHost("127.0.0.1")).toBe(true);
		expect(isPrivateHost("169.254.1.1")).toBe(true);
		expect(isPrivateHost("localhost")).toBe(true);
		expect(isPrivateHost("::1")).toBe(true);
		expect(isPrivateHost("fe80::1")).toBe(true);
		expect(isPrivateHost("fc00::1")).toBe(true);
		expect(isPrivateHost("fd12::1")).toBe(true);
	});

	test("does not flag public addresses or plain DNS names", () => {
		expect(isPrivateHost("9.9.9.9")).toBe(false);
		expect(isPrivateHost("172.32.0.1")).toBe(false); // just outside 172.16.0.0/12
		expect(isPrivateHost("192.169.1.1")).toBe(false);
		expect(isPrivateHost("2001:db8::1")).toBe(false);
		expect(isPrivateHost("box.example.com")).toBe(false);
	});
});

describe("orderedHints", () => {
	test("promotes the winner to the front and keeps the rest in parsed order", () => {
		const addrs = [
			{ host: "192.168.1.5", port: 8443 },
			{ host: "9.9.9.9", port: 8443 },
			{ host: "10.0.0.9", port: 8443 },
		];
		expect(orderedHints(addrs, { host: "9.9.9.9", port: 8443 })).toEqual([
			"9.9.9.9:8443",
			"192.168.1.5:8443",
			"10.0.0.9:8443",
		]);
	});

	test("brackets an IPv6 host the way pair-string.ts's grammar requires", () => {
		expect(orderedHints([{ host: "::1", port: 443 }], { host: "::1", port: 443 })).toEqual(["[::1]:443"]);
	});

	test("drops duplicate hints", () => {
		const addrs = [
			{ host: "1.2.3.4", port: 1 },
			{ host: "1.2.3.4", port: 1 },
			{ host: "5.6.7.8", port: 2 },
		];
		expect(orderedHints(addrs, { host: "5.6.7.8", port: 2 })).toEqual(["5.6.7.8:2", "1.2.3.4:1"]);
	});
});

describe("racePairAddresses", () => {
	test("a private candidate wins even though the public one was already about to answer", async () => {
		const calls: string[] = [];
		const probe: ProbeFn = async (host) => {
			calls.push(host);
			if (host === "192.168.1.5") return { fingerprint: WANT };
			return { fingerprint: "not-it" };
		};
		const addrs = [
			{ host: "9.9.9.9", port: 8443 },
			{ host: "192.168.1.5", port: 8443 },
		];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 20, timeoutMs: 500 });
		expect(outcome).toEqual({ status: "matched", host: "192.168.1.5", port: 8443 });
		// The private candidate must have been dispatched without waiting for
		// the public one's head start.
		expect(calls).toContain("192.168.1.5");
	});

	test("a wrong-fingerprint candidate is skipped silently and the race continues to a winner", async () => {
		const probe: ProbeFn = async (host) => (host === "10.0.0.1" ? { fingerprint: "wrong" } : { fingerprint: WANT });
		const addrs = [
			{ host: "10.0.0.1", port: 8443 },
			{ host: "10.0.0.2", port: 8443 },
		];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 500 });
		expect(outcome).toEqual({ status: "matched", host: "10.0.0.2", port: 8443 });
	});

	test("an unreachable candidate is recorded and does not stop the race", async () => {
		const probe: ProbeFn = async (host) => (host === "10.0.0.1" ? { error: "No certificate could be retrieved." } : { fingerprint: WANT });
		const addrs = [
			{ host: "10.0.0.1", port: 8443 },
			{ host: "10.0.0.2", port: 8443 },
		];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 500 });
		expect(outcome).toEqual({ status: "matched", host: "10.0.0.2", port: 8443 });
	});

	test("exhausts fast, well before the timeout, when every candidate fails", async () => {
		const probe: ProbeFn = async () => ({ error: "unreachable" });
		const addrs = [
			{ host: "10.0.0.1", port: 8443 },
			{ host: "10.0.0.2", port: 8443 },
		];
		const start = Date.now();
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 2000 });
		const elapsed = Date.now() - start;
		expect(outcome.status).toBe("exhausted");
		if (outcome.status === "exhausted") {
			expect(outcome.attempts).toEqual(
				expect.arrayContaining([
					{ host: "10.0.0.1", port: 8443, outcome: "unreachable" },
					{ host: "10.0.0.2", port: 8443, outcome: "unreachable" },
				]),
			);
			expect(outcome.attempts).toHaveLength(2);
		}
		expect(elapsed).toBeLessThan(1000);
	});

	test("times out with per-address attempts when nothing ever answers", async () => {
		const probe: ProbeFn = () => new Promise(() => undefined); // never resolves
		const addrs = [{ host: "10.0.0.1", port: 8443 }];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 30 });
		expect(outcome).toEqual({
			status: "exhausted",
			attempts: [{ host: "10.0.0.1", port: 8443, outcome: "unreachable" }],
		});
	});

	test("a duplicate address only contributes one attempt-worth of noise and a late resolver after a win changes nothing", async () => {
		let resolveSlow: (v: { fingerprint: string } | { error: string }) => void = () => undefined;
		const slow = new Promise<{ fingerprint: string } | { error: string }>((resolve) => {
			resolveSlow = resolve;
		});
		const probe: ProbeFn = async (host) => {
			if (host === "10.0.0.1") return slow;
			return { fingerprint: WANT };
		};
		const addrs = [
			{ host: "10.0.0.1", port: 8443 },
			{ host: "10.0.0.1", port: 8443 },
			{ host: "10.0.0.2", port: 8443 },
		];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 500 });
		expect(outcome).toEqual({ status: "matched", host: "10.0.0.2", port: 8443 });
		// Resolve the slow duplicate after the race already settled: must not throw
		// and must not change the outcome already returned above.
		resolveSlow({ fingerprint: "wrong" });
		await new Promise((r) => setTimeout(r, 10));
	});

	test("an already-aborted signal cancels the race before any probe runs", async () => {
		const probe = vi.fn<ProbeFn>(async () => ({ fingerprint: WANT }));
		const controller = new AbortController();
		controller.abort();
		const addrs = [{ host: "10.0.0.1", port: 8443 }];
		const outcome = await racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 500, signal: controller.signal });
		expect(outcome).toEqual({ status: "cancelled" });
		expect(probe).not.toHaveBeenCalled();
	});

	test("aborting mid-race cancels immediately, and the would-be winner resolving afterward changes nothing", async () => {
		let resolveWinner: (v: { fingerprint: string } | { error: string }) => void = () => undefined;
		const pending = new Promise<{ fingerprint: string } | { error: string }>((resolve) => {
			resolveWinner = resolve;
		});
		const probe: ProbeFn = async () => pending;
		const controller = new AbortController();
		const addrs = [{ host: "10.0.0.1", port: 8443 }];

		const racePromise = racePairAddresses(addrs, WANT, probe, { headStartMs: 5, timeoutMs: 2000, signal: controller.signal });
		controller.abort();
		const outcome = await racePromise;
		expect(outcome).toEqual({ status: "cancelled" });

		// The probe that would have won answers only now, well after cancellation:
		// must not throw and must not retroactively change the settled outcome.
		resolveWinner({ fingerprint: WANT });
		await new Promise((r) => setTimeout(r, 10));
	});

	test("passes the race abort signal to every started probe", async () => {
		const seenSignals: AbortSignal[] = [];
		const probe: ProbeFn = (_host, _port, signal) =>
			new Promise((_resolve, reject) => {
				if (!signal) return reject(new Error("missing signal"));
				seenSignals.push(signal);
				signal.addEventListener("abort", () => reject(new Error("cancelled")), { once: true });
			});
		const controller = new AbortController();
		const race = racePairAddresses(
			[
				{ host: "10.0.0.1", port: 8443 },
				{ host: "10.0.0.2", port: 8443 },
			],
			WANT,
			probe,
			{ signal: controller.signal },
		);
		controller.abort();

		await expect(race).resolves.toEqual({ status: "cancelled" });
		expect(seenSignals).toHaveLength(2);
		expect(seenSignals.every((signal) => signal.aborted)).toBe(true);
	});

	test("aborts losing probes as soon as another candidate wins", async () => {
		let loserSignal: AbortSignal | undefined;
		const probe: ProbeFn = (host, _port, signal) => {
			if (host === "10.0.0.2") return Promise.resolve({ fingerprint: WANT });
			loserSignal = signal;
			return new Promise((_resolve, reject) => {
				signal?.addEventListener("abort", () => reject(new Error("cancelled")), { once: true });
			});
		};

		await expect(
			racePairAddresses(
				[
					{ host: "10.0.0.1", port: 8443 },
					{ host: "10.0.0.2", port: 8443 },
				],
				WANT,
				probe,
			),
		).resolves.toEqual({ status: "matched", host: "10.0.0.2", port: 8443 });
		expect(loserSignal?.aborted).toBe(true);
	});
});
