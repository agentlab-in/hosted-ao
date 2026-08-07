export const REMOTE_PAIRING_COOKIE_NAME = "ao_hosted_pair";

export type RemoteDaemonConfig = { baseUrl: string; token: string };

export function readRemoteDaemonConfig(env: Record<string, string | undefined>): RemoteDaemonConfig | null {
	const rawURL = env.AO_REMOTE_URL?.trim() ?? "";
	const token = env.AO_REMOTE_TOKEN?.trim() ?? "";
	if (!rawURL && !token) return null;
	if (!rawURL || !token) throw new Error("AO_REMOTE_URL and AO_REMOTE_TOKEN must be set together");
	if (!/^[A-Za-z0-9_-]+$/.test(token)) throw new Error("AO_REMOTE_TOKEN must be URL-safe base64");
	const url = new URL(rawURL);
	if (url.protocol !== "https:" || url.pathname !== "/" || rawURL.includes("?") || rawURL.includes("#") || url.username || url.password) {
		throw new Error("AO_REMOTE_URL must be an HTTPS origin without a path, query, fragment, or credentials");
	}
	return { baseUrl: url.origin, token };
}

export function isRemoteDaemonBaseUrl(baseUrl: string): boolean {
	return baseUrl.startsWith("https://");
}
