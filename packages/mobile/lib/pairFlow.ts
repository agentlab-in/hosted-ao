import { DEFAULT_CONFIG, type ServerConfig } from "./config";
import { applyPairingPayload, parsePairingPayload } from "./pairing";
import { parsePairingCode } from "./pairingCode";

export type PairResult =
	| { ok: true; config: ServerConfig }
	| { ok: false; reason: "not-ao-qr" | "v2-unavailable" | "auth" | "rate-limited" | "verify-failed" | "cancelled" };

export type PairDeps = {
	verify: (config: ServerConfig, signal?: AbortSignal) => Promise<void>;
	persist: (config: ServerConfig) => Promise<void>;
};

/** Preserve frozen-downstream authenticated v1 pairing. Never release a v2 token. */
export async function pairFromCode(raw: string, deps: PairDeps, signal?: AbortSignal): Promise<PairResult> {
	if (parsePairingCode(raw)) return { ok: false, reason: "v2-unavailable" };
	const parsed = parsePairingPayload(raw);
	if (!parsed) return { ok: false, reason: "not-ao-qr" };
	if (!parsed.password) return { ok: false, reason: "auth" };
	// Start from defaults, never inherit another endpoint's saved password or v2 metadata.
	const config = applyPairingPayload(DEFAULT_CONFIG, { ...parsed, host: parsed.host.trim() });
	if (signal?.aborted) return { ok: false, reason: "cancelled" };
	try {
		await deps.verify(config, signal);
		if (signal?.aborted) return { ok: false, reason: "cancelled" };
		await deps.persist(config);
		if (signal?.aborted) return { ok: false, reason: "cancelled" };
		return { ok: true, config };
	} catch (error) {
		if (signal?.aborted) return { ok: false, reason: "cancelled" };
		const status = (error as { status?: number })?.status;
		return { ok: false, reason: status === 401 || status === 403 ? "auth"
			: status === 429 ? "rate-limited" : "verify-failed" };
	}
}
