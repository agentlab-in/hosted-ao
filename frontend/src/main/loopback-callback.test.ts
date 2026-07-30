// @vitest-environment node
import { expect, test } from "vitest";
import { startLoopbackCallback } from "./loopback-callback";

// These hit a real listener, but it is this process's own loopback socket on an
// ephemeral port, the same shape as a Go httptest server. No external network.
async function get(url: string): Promise<{ status: number; body: string }> {
	const res = await fetch(url);
	return { status: res.status, body: await res.text() };
}

test("binds loopback on an ephemeral port and hands back the matching code", async () => {
	const callback = await startLoopbackCallback("st");
	const redirect = new URL(callback.redirectUri);
	expect(redirect.hostname).toBe("127.0.0.1");
	expect(Number(redirect.port)).toBeGreaterThan(0);
	expect(redirect.pathname).toBe("/callback");

	const page = await get(`${callback.redirectUri}?code=abc&state=st`);
	expect(page.status).toBe(200);
	await expect(callback.code).resolves.toBe("abc");

	// Shut down immediately after the one callback it accepts.
	await expect(fetch(callback.redirectUri)).rejects.toThrow();
});

test("rejects a callback whose state does not match and never resolves a code", async () => {
	// Long enough that the request below finishes before the listener gives up.
	const callback = await startLoopbackCallback("st", 300);
	const page = await get(`${callback.redirectUri}?code=abc&state=forged`);
	expect(page.status).toBe(400);
	// The forged code is never handed out: the listener waits for the real
	// callback and gives up on its own timeout instead.
	await expect(callback.code).rejects.toThrow(/timed out/);
});

// The hazard: a wrong-state hit is anything on this machine that touches the
// port, including a browser prefetch, a scanner, or a page walking the ephemeral
// range. Ending the sign-in on one would let any of those abort every login.
test("keeps waiting after a wrong-state hit, and still accepts the real callback", async () => {
	const callback = await startLoopbackCallback("st");

	expect((await get(`${callback.redirectUri}?code=stolen&state=forged`)).status).toBe(400);
	expect((await get(`${callback.redirectUri}?code=stolen`)).status).toBe(400);
	expect((await get(`${callback.redirectUri}?error=access_denied&state=forged`)).status).toBe(400);

	const page = await get(`${callback.redirectUri}?code=abc&state=st`);
	expect(page.status).toBe(200);
	await expect(callback.code).resolves.toBe("abc");
});

test("ends the sign-in on an OAuth error that did carry our state", async () => {
	const callback = await startLoopbackCallback("st");
	const page = await get(`${callback.redirectUri}?error=access_denied&state=st`);
	expect(page.status).toBe(400);
	await expect(callback.code).rejects.toThrow(/declined/);
});

test("escapes the authorization server's error description into the page", async () => {
	const callback = await startLoopbackCallback("st");
	const description = encodeURIComponent("<script>alert(1)</script>");
	const page = await get(`${callback.redirectUri}?error=server_error&error_description=${description}&state=st`);
	expect(page.body).not.toContain("<script>");
	expect(page.body).toContain("&lt;script&gt;");
	await expect(callback.code).rejects.toThrow();
});

test("ignores a request on any other path without consuming the one callback", async () => {
	const callback = await startLoopbackCallback("st");
	const origin = new URL(callback.redirectUri).origin;
	expect((await get(`${origin}/favicon.ico`)).status).toBe(404);

	const page = await get(`${callback.redirectUri}?code=abc&state=st`);
	expect(page.status).toBe(200);
	await expect(callback.code).resolves.toBe("abc");
});

test("times out rather than listening forever", async () => {
	const callback = await startLoopbackCallback("st", 20);
	await expect(callback.code).rejects.toThrow(/timed out/);
	callback.close();
});

test("close() releases the port even when no callback ever arrives", async () => {
	const callback = await startLoopbackCallback("st");
	const url = callback.redirectUri;
	callback.close();
	await expect(fetch(url)).rejects.toThrow();
	// Idempotent: the flow's finally block may close an already-closed listener.
	expect(() => callback.close()).not.toThrow();
});
