// @vitest-environment node
import { expect, test, vi } from "vitest";
import { TOKEN_PATH } from "./ao-pkce";
import { runDesktopLogin, type DesktopLoginDeps } from "./ao-login";

const CONTROL_PLANE = "https://ao.agentlab.in";

function fakeCallback(outcome: string | Error): DesktopLoginDeps["startCallback"] {
	return async () => ({
		redirectUri: "http://127.0.0.1:54321/callback",
		code: outcome instanceof Error ? Promise.reject(outcome) : Promise.resolve(outcome),
		close: () => undefined,
	});
}

function deps(overrides: Partial<DesktopLoginDeps> = {}): DesktopLoginDeps {
	return {
		controlPlaneUrl: CONTROL_PLANE,
		openExternal: async () => undefined,
		startCallback: fakeCallback("auth_code_1"),
		...overrides,
	};
}

function tokenResponse(body: Record<string, unknown>, status = 200) {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

test("exchanges the authorization code for the account and refresh token", async () => {
	const fetchImpl = vi.fn(async () =>
		tokenResponse({ refresh_token: "rt_1", account: { id: "acct_1", email: "dev@example.com" } }),
	);

	await expect(runDesktopLogin(deps({ fetchImpl: fetchImpl as unknown as typeof fetch }))).resolves.toEqual({
		account: { id: "acct_1", email: "dev@example.com" },
		refreshToken: "rt_1",
	});

	const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
	expect(url).toBe(`${CONTROL_PLANE}${TOKEN_PATH}`);
	expect(new URLSearchParams(init.body as string).get("code")).toBe("auth_code_1");
	expect(new URLSearchParams(init.body as string).get("grant_type")).toBe("authorization_code");
});

test("a rejected sign-in never reaches the token endpoint", async () => {
	const fetchImpl = vi.fn();
	await expect(
		runDesktopLogin(
			deps({
				startCallback: fakeCallback(new Error("access_denied")),
				fetchImpl: fetchImpl as unknown as typeof fetch,
			}),
		),
	).rejects.toThrow(/access_denied/);
	expect(fetchImpl).not.toHaveBeenCalled();
});

// Regression: exchangeCode used to be the only control-plane call in the app
// with no AbortController. ao-account.ts's sign-in `inFlight` is a single
// promise cleared only in a `finally`, so a blackholed token POST would
// otherwise never settle and would poison sign-in for the rest of the
// process's life, exactly the hazard ao-control-token.ts's own `inFlight`
// already guards against. See request-deadline.ts.
test("a stalled token exchange times out rather than hanging forever", async () => {
	// Ignores the abort signal entirely, the worst case fetchWithDeadline's race
	// still has to survive: nothing but the race in request-deadline.ts rescues it.
	const stalls: typeof fetch = () => new Promise<Response>(() => {});

	await expect(runDesktopLogin(deps({ fetchImpl: stalls, timeoutMs: 20 }))).rejects.toThrow(
		/Could not reach the AO control plane/,
	);
});
