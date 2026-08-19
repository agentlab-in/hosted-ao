// Concurrent candidate racing for the paste-first add-machine flow
// (frontend/src/renderer/components/settings/AddPairedMachineDialog.tsx):
// every address a pairing string lists is tried at once, and the first one
// whose presented fingerprint matches the string's wins. Pure and
// Electron-free like the rest of this directory: the actual TLS probe is
// injected as `probe`, so this module only owns the race's timing and
// bookkeeping, and is exercised directly by pair-race.test.ts rather than
// only indirectly through the dialog's component tests.
//
// See docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md.

import type { PairAddr } from "./pair-string";

export type ProbeFn = (host: string, port: number) => Promise<{ fingerprint: string } | { error: string }>;

export type RaceAttempt = { host: string; port: number; outcome: "mismatch" | "unreachable" };

export type RaceOutcome = { status: "matched"; host: string; port: number } | { status: "exhausted"; attempts: RaceAttempt[] };

export type RaceOptions = {
	/** Delay before a non-private candidate starts, so a private one (same
	 * network as this computer, usually a few ms of latency) gets to resolve
	 * first without ever bothering a public address. */
	headStartMs?: number;
	/** Overall budget for the whole race, win or nothing. */
	timeoutMs?: number;
	isPrivate?: (host: string) => boolean;
};

const DEFAULT_HEAD_START_MS = 250;
const DEFAULT_TIMEOUT_MS = 10_000;

/** RFC 1918 + loopback-adjacent private-network detection for a parsed
 * pairing string's bare host (never bracketed here; PairAddr already strips
 * IPv6 brackets). Good enough to bias the race toward the local network;
 * anything this does not recognize (a public IP or a DNS name) races with no
 * head start rather than risk delaying a legitimate address it cannot
 * classify. */
export function isPrivateHost(host: string): boolean {
	const h = host.toLowerCase();
	if (h === "localhost") return true;

	const ipv4 = h.match(/^(\d{1,3})\.(\d{1,3})\.\d{1,3}\.\d{1,3}$/);
	if (ipv4) {
		const a = Number(ipv4[1]);
		const b = Number(ipv4[2]);
		if (a === 127) return true; // loopback
		if (a === 10) return true; // 10.0.0.0/8
		if (a === 172 && b >= 16 && b <= 31) return true; // 172.16.0.0/12
		if (a === 192 && b === 168) return true; // 192.168.0.0/16
		if (a === 169 && b === 254) return true; // link-local
		return false;
	}

	if (h.includes(":")) {
		if (h === "::1") return true; // loopback
		if (h.startsWith("fe80:")) return true; // link-local
		if (h.startsWith("fc") || h.startsWith("fd")) return true; // unique local, fc00::/7
		return false;
	}

	return false;
}

/** Race every address concurrently: a private one starts immediately, a
 * public one waits out `headStartMs` first, so a reachable box on the local
 * network always gets a shot before any address leaves it. The first probe
 * whose fingerprint equals `wantFingerprint` (already normalized to
 * `toPinnedFingerprintFormat`) wins; a wrong fingerprint or an unreachable
 * address is recorded and the race continues, silently, since discovery
 * traffic on a LAN is not an attack. Resolves the instant one candidate
 * wins, every candidate is exhausted, or `timeoutMs` elapses, whichever
 * comes first; a probe that answers after the race has already settled is
 * ignored, so a slow loser can never double-resolve or overwrite a win. */
export function racePairAddresses(addrs: PairAddr[], wantFingerprint: string, probe: ProbeFn, opts: RaceOptions = {}): Promise<RaceOutcome> {
	const headStartMs = opts.headStartMs ?? DEFAULT_HEAD_START_MS;
	const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
	const isPrivate = opts.isPrivate ?? isPrivateHost;

	return new Promise((resolve) => {
		const attempts: RaceAttempt[] = [];
		let settled = false;
		let pending = addrs.length;
		const timers: ReturnType<typeof setTimeout>[] = [];

		const finish = (outcome: RaceOutcome) => {
			if (settled) return;
			settled = true;
			for (const timer of timers) clearTimeout(timer);
			resolve(outcome);
		};

		if (addrs.length === 0) {
			finish({ status: "exhausted", attempts });
			return;
		}

		timers.push(setTimeout(() => finish({ status: "exhausted", attempts }), timeoutMs));

		for (const addr of addrs) {
			const start = () => {
				probe(addr.host, addr.port)
					.then((result) => {
						if (settled) return;
						if ("fingerprint" in result && result.fingerprint === wantFingerprint) {
							finish({ status: "matched", host: addr.host, port: addr.port });
							return;
						}
						attempts.push({ host: addr.host, port: addr.port, outcome: "fingerprint" in result ? "mismatch" : "unreachable" });
						pending--;
						if (pending === 0) finish({ status: "exhausted", attempts });
					})
					.catch(() => {
						if (settled) return;
						attempts.push({ host: addr.host, port: addr.port, outcome: "unreachable" });
						pending--;
						if (pending === 0) finish({ status: "exhausted", attempts });
					});
			};
			if (isPrivate(addr.host)) start();
			else timers.push(setTimeout(start, headStartMs));
		}
	});
}

/** "host:port" hints in the pairing string's parsed order, with the winning
 * candidate promoted to the front and duplicates dropped -- the same
 * front-of-list convention `promoteAddress` (frontend/src/main/paired-machines.ts)
 * keeps on every later reconnect. Bracket formatting matches the grammar
 * pair-string.ts parses, so a hint here round-trips through it unchanged. */
export function orderedHints(addrs: PairAddr[], winner: { host: string; port: number }): string[] {
	const format = (addr: { host: string; port: number }) => (addr.host.includes(":") ? `[${addr.host}]:${addr.port}` : `${addr.host}:${addr.port}`);
	const winnerHint = format(winner);
	const rest = addrs.map(format).filter((hint) => hint !== winnerHint);
	return [winnerHint, ...Array.from(new Set(rest))];
}
