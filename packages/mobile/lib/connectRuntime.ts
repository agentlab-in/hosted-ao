/** Frozen-downstream single-endpoint compatibility. V2 identity racing is unavailable. */
import { fetch } from "expo/fetch";
import type { ConnectDeps, ConnectResult } from "./connect";
import type { ServerConfig } from "./config";
import { isLocalNetworkHost, isTailscaleHost } from "./connectionError";
import type { Endpoint } from "./endpoints";
import { adoptHostIdentity, findHost, touchHost, updateHostEndpoints } from "./hosts";
import type { ProbeAnswer } from "./race";

export const PROBE_TIMEOUT_MS = 3_000;

export class MobileConnectionError extends Error {
	constructor(readonly status: number) {
		super(`Connection request returned ${status}`);
	}
}

/** Neither anonymous identity nor password-before-identity is permitted for v2. */
export async function probeEndpoint(_endpoint: Endpoint, _signal: AbortSignal): Promise<ProbeAnswer> {
	throw new Error("QR v2 pairing is unavailable in this build.");
}

export async function probeIdentity(_cfg: {
	host: string; httpPort: string; secure?: boolean;
}): Promise<string> {
	throw new Error("QR v2 pairing is unavailable in this build.");
}

/** Retain exported dependencies for callers, but never activate the upstream race. */
export function runtimeConnectDeps(): ConnectDeps {
	return {
		findHost,
		race: async () => ({ ok: false, reason: "no-candidates" }),
		refreshEndpoints: async () => { throw new Error("QR v2 pairing is unavailable in this build."); },
		saveEndpoints: updateHostEndpoints,
		adoptIdentity: adoptHostIdentity,
		touch: touchHost,
	};
}

export async function connectToHost(_hostId: string): Promise<ConnectResult> {
	return { ok: false, reason: "no-candidates" };
}

/** The v1 single-server config has no host identity or raced endpoint metadata. */
export function isLegacyConnection(config: ServerConfig): boolean {
	return config.hostId === undefined && config.endpointKind === undefined;
}

/** Frozen v1 verifies the authenticated application API, without requesting identity.
 * Restrict this compatibility path to the existing home-network/Tailscale scope. */
export async function verifyLegacyConnection(config: ServerConfig, signal?: AbortSignal): Promise<void> {
	if (!isLegacyConnection(config)) throw new Error("QR v2 pairing is unavailable in this build.");
	if (!config.password) throw new MobileConnectionError(401);
	const host = config.host.trim();
	const port = Number(config.httpPort);
	if (!/^[a-zA-Z0-9.-]+$/.test(host) || !Number.isInteger(port) || port < 1 || port > 65535 ||
		!(isLocalNetworkHost(host) || isTailscaleHost(host) || /^[a-zA-Z0-9.-]+\.ts\.net$/.test(host))) {
		throw new Error("Use the address from Connect Mobile on your trusted home network or Tailscale.");
	}
	if (signal?.aborted) throw new Error("Connection cancelled");
	const controller = new AbortController();
	const onAbort = () => controller.abort();
	signal?.addEventListener("abort", onAbort, { once: true });
	const timeout = setTimeout(onAbort, PROBE_TIMEOUT_MS);
	try {
		const res = await fetch(`${config.secure ? "https" : "http"}://${host}:${port}/api/v1/sessions`, {
			method: "GET", headers: { Authorization: `Bearer ${config.password}` },
			redirect: "error", credentials: "omit", signal: controller.signal,
		});
		if (!res.ok) throw new MobileConnectionError(res.status);
		await res.json();
		if (controller.signal.aborted) throw new Error("Connection cancelled");
	} finally {
		clearTimeout(timeout);
		controller.abort();
		signal?.removeEventListener("abort", onAbort);
	}
}
