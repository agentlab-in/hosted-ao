import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { parseCallback } from "./ao-pkce";

/**
 * The RFC 8252 loopback redirect receiver for desktop login.
 *
 * Binds 127.0.0.1 on an ephemeral port (never 0.0.0.0: the redirect is for this
 * machine only), accepts exactly one callback, validates `state` before touching
 * the code, and shuts down immediately afterwards so nothing is left listening.
 */

export const CALLBACK_PATH = "/callback";

/** How long the browser has to come back before the listener gives up. */
export const CALLBACK_TIMEOUT_MS = 5 * 60 * 1000;

export type LoopbackCallback = {
	/** The exact redirect_uri to send in the authorization request. */
	redirectUri: string;
	/** Resolves with the authorization code, rejects on a bad or absent callback. */
	code: Promise<string>;
	/** Idempotent. Safe to call after the callback has already closed the listener. */
	close: () => void;
};

const PAGE = (heading: string, body: string) =>
	`<!doctype html><meta charset="utf-8"><title>${heading}</title>` +
	`<body style="font:15px system-ui;margin:14vh auto;max-width:26rem;text-align:center">` +
	`<h1 style="font-size:1.1rem">${heading}</h1><p style="color:#666">${body}</p></body>`;

function respond(res: ServerResponse, status: number, heading: string, body: string): void {
	res.writeHead(status, { "content-type": "text/html; charset=utf-8", connection: "close" });
	res.end(PAGE(heading, body));
}

export async function startLoopbackCallback(
	expectedState: string,
	timeoutMs = CALLBACK_TIMEOUT_MS,
): Promise<LoopbackCallback> {
	let settle: ((result: { code: string } | { error: Error }) => void) | null = null;
	const code = new Promise<string>((resolve, reject) => {
		settle = (result) => ("code" in result ? resolve(result.code) : reject(result.error));
	});
	// Swallow the rejection until the caller awaits `code`: the flow can reject
	// while the caller is still awaiting the browser open.
	code.catch(() => undefined);

	let done = false;
	const finish = (result: { code: string } | { error: Error }): void => {
		if (done) return;
		done = true;
		clearTimeout(timer);
		settle?.(result);
		close();
	};

	const server = createServer((req: IncomingMessage, res: ServerResponse) => {
		// One callback only. Anything else, including a favicon fetch or a second
		// hit on an already-used code, gets 404 and changes no state.
		const url = req.url ?? "";
		if (done || !url.startsWith(CALLBACK_PATH)) {
			respond(res, 404, "Not found", "This local sign-in listener has nothing to serve.");
			return;
		}
		const result = parseCallback(url, expectedState);
		if ("error" in result) {
			respond(res, 400, "Sign-in was rejected", result.error);
			finish({ error: new Error(result.error) });
			return;
		}
		respond(res, 200, "You are signed in", "You can close this tab and go back to Agent Orchestrator.");
		finish({ code: result.code });
	});

	const timer = setTimeout(() => {
		finish({ error: new Error("Sign-in timed out waiting for the browser.") });
	}, timeoutMs);
	// Never hold app exit open on the login listener.
	timer.unref?.();

	function close(): void {
		clearTimeout(timer);
		// closeAllConnections drops the keep-alive socket the browser may still be
		// holding, so the port is released now rather than whenever the browser lets go.
		server.closeAllConnections?.();
		server.close();
	}

	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, "127.0.0.1", resolve);
	});

	const address = server.address() as AddressInfo | null;
	if (!address) {
		close();
		throw new Error("Could not open a local sign-in listener.");
	}

	return { redirectUri: `http://127.0.0.1:${address.port}${CALLBACK_PATH}`, code, close };
}
