// @vitest-environment node
import net, { type Server as NetServer, type Socket } from "node:net";
import tls, { type Server as TLSServer, type TLSSocket } from "node:tls";
import { afterEach, describe, expect, test, vi } from "vitest";
import { canonicalPairHost, capturePairFingerprint } from "./paired-machine-probe";

// Public, test-only PKCS#12 fixture. Keeping the inert fixture as base64 avoids
// making a PEM private-key marker look like a production credential to secret
// scanners. Password: pair-probe-test.
const TEST_PFX_BASE64 =
	"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";
const TEST_FINGERPRINT = "98:5A:BA:63:91:0E:21:8A:06:A8:C3:05:83:A2:89:C0:60:DB:70:42:46:71:F7:93:90:4F:44:CF:52:EA:25:E8";

const servers: Array<NetServer | TLSServer> = [];
const sockets = new Set<Socket | TLSSocket>();

afterEach(async () => {
	for (const socket of sockets) socket.destroy();
	await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => server.close(() => resolve()))));
	sockets.clear();
});

async function listen(server: NetServer | TLSServer): Promise<number> {
	servers.push(server);
	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, "127.0.0.1", () => {
			server.removeListener("error", reject);
			resolve();
		});
	});
	const address = server.address();
	if (!address || typeof address === "string") throw new Error("test server did not bind a TCP port");
	return address.port;
}

function tlsServer(): TLSServer {
	const server = tls.createServer(
		{ pfx: Buffer.from(TEST_PFX_BASE64, "base64"), passphrase: "pair-probe-test" },
		(socket) => {
			sockets.add(socket);
			socket.once("close", () => sockets.delete(socket));
		},
	);
	server.on("tlsClientError", () => undefined);
	return server;
}

describe("canonicalPairHost", () => {
	test("normalizes DNS, IPv4, and bracketed or bare IPv6 to stable keys", () => {
		expect(canonicalPairHost("BOX.Local")).toBe("box.local");
		expect(canonicalPairHost("192.168.001.005")).toBe("192.168.1.5");
		expect(canonicalPairHost("2001:0DB8:0:0:0:0:0:1")).toBe("2001:db8::1");
		expect(canonicalPairHost("[2001:db8::1]")).toBe("2001:db8::1");
	});
});

test("captures the real leaf certificate repeatedly without retaining probe state", async () => {
	const port = await listen(tlsServer());

	for (let attempt = 0; attempt < 20; attempt++) {
		await expect(capturePairFingerprint("127.0.0.1", port, { timeoutMs: 500 })).resolves.toBe(TEST_FINGERPRINT);
	}
	await new Promise<void>((resolve) => setImmediate(resolve));
	expect(sockets.size).toBe(0);
});

test("concurrent captures settle independently", async () => {
	const port = await listen(tlsServer());

	const captures = Array.from({ length: 8 }, () =>
		capturePairFingerprint("127.0.0.1", port, { timeoutMs: 500 }),
	);
	await expect(Promise.all(captures)).resolves.toEqual(Array(8).fill(TEST_FINGERPRINT));
});

test("timeout destroys a socket whose peer never starts TLS", async () => {
	const server = net.createServer((socket) => {
		sockets.add(socket);
		socket.once("close", () => sockets.delete(socket));
		socket.resume();
	});
	const port = await listen(server);

	await expect(capturePairFingerprint("127.0.0.1", port, { timeoutMs: 20 })).rejects.toThrow("timed out");
	await vi.waitFor(() => expect(sockets.size).toBe(0));
});

test("abort destroys an in-flight socket", async () => {
	const server = net.createServer((socket) => {
		sockets.add(socket);
		socket.once("close", () => sockets.delete(socket));
		socket.resume();
	});
	const port = await listen(server);
	const controller = new AbortController();
	const capture = capturePairFingerprint("127.0.0.1", port, { timeoutMs: 500, signal: controller.signal });
	controller.abort();

	await expect(capture).rejects.toThrow("cancelled");
	await vi.waitFor(() => expect(sockets.size).toBe(0));
});
