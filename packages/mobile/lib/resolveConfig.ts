import { loadConfig, type ServerConfig } from "./config";
import { isLegacyConnection, verifyLegacyConnection } from "./connectRuntime";

export type ResolveDeps = {
	loadLegacyConfig: () => Promise<ServerConfig>;
	verify: (config: ServerConfig, signal?: AbortSignal) => Promise<void>;
};

/** Reconnect the frozen-downstream single-server config, without migration or identity races.
 * V2-derived stored configs remain unavailable; never fall back around that gate. */
export async function resolveActiveConfig(deps: ResolveDeps, signal?: AbortSignal): Promise<ServerConfig | null> {
	try {
		const config = await deps.loadLegacyConfig();
		if (!config.host || !config.password || !isLegacyConnection(config) || signal?.aborted) return null;
		await deps.verify(config, signal);
		return signal?.aborted ? null : config;
	} catch {
		return null;
	}
}

export function runtimeResolveDeps(): ResolveDeps {
	return { loadLegacyConfig: loadConfig, verify: verifyLegacyConnection };
}
