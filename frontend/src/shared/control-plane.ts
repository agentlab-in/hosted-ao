/**
 * Which AO control plane the desktop app trusts.
 *
 * `AO_CONTROL_URL` is the development hatch for pointing the app at a
 * locally-run control plane (`go run ./cmd/controlplane`, default
 * `127.0.0.1:8080`). It selects WHICH control plane issues and signs tokens; it
 * can never skip authentication. There is deliberately no env var and no flag
 * that bypasses login: sign-in always runs the same PKCE flow against whatever
 * origin this resolves to.
 */
export const DEFAULT_CONTROL_PLANE_URL = "https://ao.agentlab.in";

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "::1", "localhost"]);

/** Whether a URL's hostname is this machine. `[::1]` arrives bracketed from URL. */
export function isLoopbackHost(hostname: string): boolean {
	return LOOPBACK_HOSTS.has(hostname) || LOOPBACK_HOSTS.has(hostname.replace(/^\[|\]$/g, ""));
}

/**
 * Resolve the control-plane origin from the environment. Returns an origin with
 * no trailing slash so it can be concatenated with a path, matching the way the
 * control plane pins `iss` from `PUBLIC_ORIGIN` (see controlplane/TOKEN_CONTRACT.md).
 *
 * Throws on a malformed value rather than falling back to the production origin:
 * a typo in the dev hatch must not silently send a developer's login to the real
 * control plane.
 */
export function readControlPlaneUrl(env: Record<string, string | undefined>): string {
	const raw = env.AO_CONTROL_URL?.trim() ?? "";
	if (!raw) return DEFAULT_CONTROL_PLANE_URL;

	let url: URL;
	try {
		url = new URL(raw);
	} catch {
		throw new Error("AO_CONTROL_URL must be an absolute origin, for example http://127.0.0.1:8080");
	}
	if (url.pathname !== "/" || url.search || url.hash || url.username || url.password) {
		throw new Error("AO_CONTROL_URL must be an origin without a path, query, fragment, or credentials");
	}
	// Plain HTTP is allowed only for a control plane on this machine, which is the
	// one case where there is no network to intercept the authorization code.
	if (url.protocol !== "https:" && !(url.protocol === "http:" && isLoopbackHost(url.hostname))) {
		throw new Error("AO_CONTROL_URL must be HTTPS, or HTTP on 127.0.0.1 for a locally-run control plane");
	}
	return url.origin;
}
