// @vitest-environment node
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { SafeStorageLike } from "./ao-account-store";
import type { PairCertificateVerifyProc } from "./paired-machine-cert";
import { AO_PAIRED_MACHINES_FILE_NAME, createPairedMachinesController } from "./paired-machines";

// Same fixture certificate and fingerprint as paired-machine-cert.test.ts.
const CERT_PEM = `-----BEGIN CERTIFICATE-----
MIIBdDCCARmgAwIBAgIUUvaO5qV4lYJUJvB86I5+gJtYg8UwCgYIKoZIzj0EAwIw
DzENMAsGA1UEAwwEdGVzdDAeFw0yNjA4MTcwOTE2NTZaFw0yNjA4MTgwOTE2NTZa
MA8xDTALBgNVBAMMBHRlc3QwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAARlsPrG
o5uD+vjNG2xauAee0+9QvTvujnrf0+jC6fsmopqPMvkotQ2IiiJloA94aQHO3gB5
ZmRdPTOaOy2Jry4Xo1MwUTAdBgNVHQ4EFgQUIeE+Q4oqUd9Kut/ztrqizj8omBMw
HwYDVR0jBBgwFoAUIeE+Q4oqUd9Kut/ztrqizj8omBMwDwYDVR0TAQH/BAUwAwEB
/zAKBggqhkjOPQQDAgNJADBGAiEAn9NMvNxBzBZSI/67DaytKRMz5LACGlwoketR
rJZoaUICIQCkpVhVKVw/W7rEW+nTMAha8AzRrC4gh02oEc6gudhp/w==
-----END CERTIFICATE-----`;
const CERT_FINGERPRINT = "DF:9A:6C:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";

const OTHER_CERT_PEM = `-----BEGIN CERTIFICATE-----
MIIBdDCCARugAwIBAgIUMzz/EDkOedEV76i5gCgRFg60xv0wCgYIKoZIzj0EAwIw
EDEOMAwGA1UEAwwFdGVzdDIwHhcNMjYwODE3MDkyMjE3WhcNMjYwODE4MDkyMjE3
WjAQMQ4wDAYDVQQDDAV0ZXN0MjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABE2K
jZbJkEjs1Q2uNJfuYpG9at/geTaamvrcza7xJo2YS0Gpiov3t1uLddItplpb6DeE
0HKq9PnUQO6ZXZNMsQOjUzBRMB0GA1UdDgQWBBQGPekCiEsYWCbL93gvmUEgeacp
9TAfBgNVHSMEGDAWgBQGPekCiEsYWCbL93gvmUEgeacp9TAPBgNVHRMBAf8EBTAD
AQH/MAoGCCqGSM49BAMCA0cAMEQCIBrjUHRvpztw9S63YzDQrXHjUiow5PQePgOc
kaum9sXnAiBWQyafyurASxXHB+UORQpWHpfyWCm9gr1/x2+SjPBeQQ==
-----END CERTIFICATE-----`;

const safeStorage: SafeStorageLike = {
	isEncryptionAvailable: () => true,
	encryptString: (plain) => Buffer.from(`enc:${plain}`, "utf8"),
	decryptString: (cipher) => cipher.toString("utf8").replace(/^enc:/, ""),
};

/**
 * Stands in for Electron's net.fetch: runs the same certificate through the
 * controller's own verifyCertificate, exactly as Chromium's network service
 * would during a real TLS handshake, then resolves or rejects the way a real
 * fetch would given that verdict. This is what lets probeFingerprint and the
 * pin-enforcement paths be exercised without a real network stack.
 */
function netFetchPresenting(pem: string, verify: PairCertificateVerifyProc): typeof fetch {
	return (async (input: string | URL | Request) => {
		const url = new URL(String(input));
		let verdict: number | undefined;
		verify({ hostname: url.hostname, certificate: { data: pem } }, (result) => {
			verdict = result;
		});
		if (verdict === 0) return new Response("ok", { status: 200 });
		throw new Error("certificate verification failed");
	}) as unknown as typeof fetch;
}

/** A box that never answers at all: no TLS handshake, nothing presented. */
const unreachableFetch: typeof fetch = (async () => {
	throw new Error("connect ECONNREFUSED");
}) as unknown as typeof fetch;

let stateDir = "";

beforeEach(async () => {
	stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-paired-machines-"));
});
afterEach(async () => {
	await rm(stateDir, { recursive: true, force: true });
});

function controller(netFetch: typeof fetch = unreachableFetch) {
	return createPairedMachinesController({ stateDir, safeStorage, netFetch, probeTimeoutMs: 200 });
}

test("starts empty", async () => {
	const machines = controller();
	await machines.load();
	expect(machines.list()).toEqual([]);
});

test("probing a box with no pin yet captures the presented fingerprint without pairing it", async () => {
	// netFetch must run the SAME controller's verifyCertificate that
	// probeFingerprint marks the host pending on, exactly as Electron's
	// network service and session.setCertificateVerifyProc share one session
	// in the real app. The indirection here only exists because the
	// controller and its netFetch dependency would otherwise be a
	// construction cycle.
	let verify: PairCertificateVerifyProc | undefined;
	const netFetch = netFetchPresenting(CERT_PEM, (request, callback) => verify?.(request, callback));
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch, probeTimeoutMs: 200 });
	verify = machines.verifyCertificate;
	await machines.load();

	const result = await machines.probeFingerprint("192.168.1.5", 8443);
	expect(result).toEqual({ fingerprint: CERT_FINGERPRINT });
	// Nothing was pinned by the probe alone.
	expect(machines.list()).toEqual([]);
});

test("probing an unreachable address reports an error, not a fingerprint", async () => {
	const machines = controller(unreachableFetch);
	await machines.load();
	const result = await machines.probeFingerprint("192.168.1.9", 8443);
	expect(result).toEqual({ error: expect.stringContaining("No certificate could be retrieved") });
});

test("add persists the pairing and round-trips through disk", async () => {
	const machines = controller();
	await machines.load();

	const added = await machines.add({
		id: "box_1",
		name: "Pi in the closet",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});
	expect(added).toMatchObject({
		id: "box_1",
		name: "Pi in the closet",
		baseUrl: "https://192.168.1.5:8443",
		local: false,
		reachability: "unknown",
	});

	// Reload as a fresh controller, the way a relaunch would.
	const reloaded = createPairedMachinesController({ stateDir, safeStorage });
	await reloaded.load();
	expect(reloaded.list()).toEqual([added]);
});

test("the passcode is never written to disk in plaintext, and round-trips through safeStorage", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "super-secret-passcode",
		fingerprint: CERT_FINGERPRINT,
	});

	const onDisk = await readFile(path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME), "utf8");
	expect(onDisk).not.toContain("super-secret-passcode");

	expect(await machines.getPasscode("box_1")).toBe("super-secret-passcode");
	expect(await machines.getPasscode("no_such_machine")).toBeNull();
});

test("remove drops the machine and persists the removal", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});
	await machines.remove("box_1");
	expect(machines.list()).toEqual([]);

	const reloaded = createPairedMachinesController({ stateDir, safeStorage });
	await reloaded.load();
	expect(reloaded.list()).toEqual([]);
});

test("after pairing, the pin is enforced: the same certificate is accepted", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const callback = vi.fn();
	machines.verifyCertificate({ hostname: "192.168.1.5", certificate: { data: CERT_PEM } }, callback);
	expect(callback).toHaveBeenCalledExactlyOnceWith(0);
});

test("after pairing, a swapped certificate on the same host is refused, with no fallback", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const callback = vi.fn();
	machines.verifyCertificate({ hostname: "192.168.1.5", certificate: { data: OTHER_CERT_PEM } }, callback);
	expect(callback).toHaveBeenCalledExactlyOnceWith(-2);
});

test("a host that was never paired or probed gets default certificate verification", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	// ao.agentlab.in (the control plane) and any other unrelated host must be
	// completely unaffected by a pin that exists for a different machine.
	const callback = vi.fn();
	machines.verifyCertificate({ hostname: "ao.agentlab.in", certificate: { data: CERT_PEM } }, callback);
	expect(callback).toHaveBeenCalledExactlyOnceWith(-3);
});

test("add refuses the reserved local machine id, an out-of-range port, and an unusable address", async () => {
	const machines = controller();
	await machines.load();

	await expect(
		machines.add({ id: "local", name: "x", address: "192.168.1.5", port: 8443, passcode: "p", fingerprint: CERT_FINGERPRINT }),
	).rejects.toThrow(/machine id/);
	await expect(
		machines.add({ id: "box_1", name: "x", address: "192.168.1.5", port: 99999, passcode: "p", fingerprint: CERT_FINGERPRINT }),
	).rejects.toThrow(/valid port/);
	await expect(
		machines.add({ id: "box_1", name: "x", address: "not a host!", port: 8443, passcode: "p", fingerprint: CERT_FINGERPRINT }),
	).rejects.toThrow(/usable address/);
});
