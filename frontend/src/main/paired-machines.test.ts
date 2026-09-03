// @vitest-environment node
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { SafeStorageLike } from "./paired-machines";
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

/** A box that never answers at all: no TLS handshake, nothing presented. */
const unreachableFetch: typeof fetch = (async () => {
	throw new Error("connect ECONNREFUSED");
}) as unknown as typeof fetch;

/**
 * A box that answers `GET /api/v1/doctor` like the real gateway route would:
 * 200 with a doctor report when the Authorization header carries `passcode`
 * as a bearer, 401 otherwise. Records every Authorization header it saw, so a
 * test can assert the wire shape without inspecting the fetch call directly.
 */
function doctorFetch(passcode: string, checks: Array<Record<string, unknown>> = []) {
	const seenAuthorization: Array<string | null> = [];
	const requestedUrls: string[] = [];
	const fetchImpl = (async (input: string, init?: RequestInit) => {
		requestedUrls.push(String(input));
		seenAuthorization.push(new Headers(init?.headers).get("Authorization"));
		if (!String(input).endsWith("/api/v1/doctor")) return new Response("not found", { status: 404 });
		if (new Headers(init?.headers).get("Authorization") !== `Bearer ${passcode}`) {
			return new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 });
		}
		return new Response(JSON.stringify({ ok: true, failures: 0, checks }), { status: 200 });
	}) as unknown as typeof fetch;
	return { fetchImpl, seenAuthorization, requestedUrls };
}

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
	const fingerprintProbe = vi.fn(async () => CERT_FINGERPRINT);
	const machines = createPairedMachinesController({ stateDir, safeStorage, fingerprintProbe, probeTimeoutMs: 200 });
	await machines.load();

	const result = await machines.probeFingerprint("192.168.1.5", 8443);
	expect(result).toEqual({ fingerprint: CERT_FINGERPRINT });
	expect(fingerprintProbe).toHaveBeenCalledExactlyOnceWith("192.168.1.5", 8443, { timeoutMs: 200 });
	// Nothing was pinned by the probe alone.
	expect(machines.list()).toEqual([]);
});

test("probing an unreachable address reports an error, not a fingerprint", async () => {
	const fingerprintProbe = vi.fn(async () => Promise.reject(new Error("connect ECONNREFUSED")));
	const machines = createPairedMachinesController({ stateDir, safeStorage, fingerprintProbe, probeTimeoutMs: 200 });
	await machines.load();
	const result = await machines.probeFingerprint("192.168.1.9", 8443);
	expect(result).toEqual({ error: expect.stringContaining("No certificate could be retrieved") });
});

test("a retry after either a miss or a successful capture runs a fresh stateless probe", async () => {
	const fingerprintProbe = vi
		.fn()
		.mockRejectedValueOnce(new Error("connect ECONNREFUSED"))
		.mockResolvedValueOnce(CERT_FINGERPRINT)
		.mockResolvedValueOnce(CERT_FINGERPRINT);
	const machines = createPairedMachinesController({ stateDir, safeStorage, fingerprintProbe, probeTimeoutMs: 200 });
	await machines.load();

	expect(await machines.probeFingerprint("192.168.1.5", 8443)).toEqual({ error: expect.any(String) });
	expect(await machines.probeFingerprint("192.168.1.5", 8443)).toEqual({ fingerprint: CERT_FINGERPRINT });
	expect(await machines.probeFingerprint("192.168.1.5", 8443)).toEqual({ fingerprint: CERT_FINGERPRINT });
	expect(fingerprintProbe).toHaveBeenCalledTimes(3);
});

test("concurrent probes return their own result without stale shared capture state", async () => {
	const resolvers = new Map<string, (fingerprint: string) => void>();
	const fingerprintProbe = vi.fn(
		(address: string) => new Promise<string>((resolve) => resolvers.set(address, resolve)),
	);
	const machines = createPairedMachinesController({
		stateDir,
		safeStorage,
		fingerprintProbe,
		probeTimeoutMs: 200,
	});
	await machines.load();

	const first = machines.probeFingerprint("192.168.1.5", 8443);
	const second = machines.probeFingerprint("192.168.1.6", 8443);
	resolvers.get("192.168.1.6")?.("second");
	resolvers.get("192.168.1.5")?.("first");

	await expect(first).resolves.toEqual({ fingerprint: "first" });
	await expect(second).resolves.toEqual({ fingerprint: "second" });
});

test("fingerprint capture never touches the Electron fetch used by pinned traffic", async () => {
	const { fetchImpl } = doctorFetch("abc123XY");
	const netFetch = vi.fn(fetchImpl);
	const fingerprintProbe = vi.fn(async () => CERT_FINGERPRINT);
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch, fingerprintProbe, probeTimeoutMs: 200 });
	await machines.load();

	await machines.probeFingerprint("192.168.1.5", 8443);
	expect(netFetch).not.toHaveBeenCalled();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const refreshed = await machines.refresh();
	expect(refreshed[0]).toMatchObject({ reachability: "online" });
	expect(netFetch).toHaveBeenCalledOnce();
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

	// A bare add() with no explicit hints defaults the hint list to the single
	// current address, the same "host:port" shape a v1 migration would produce.
	expect(machines.getAddresses("box_1")).toEqual(["192.168.1.5:8443"]);

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

test("a bare IPv6 pairing address matches Electron's bracketed certificate hostname", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_ipv6",
		name: "IPv6 box",
		address: "2001:db8::1",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const callback = vi.fn();
	machines.verifyCertificate({ hostname: "[2001:0DB8:0:0:0:0:0:1]", certificate: { data: CERT_PEM } }, callback);
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

test("getPinnedFingerprint reports the pin for a registered machine and null otherwise", async () => {
	const machines = controller();
	await machines.load();
	expect(machines.getPinnedFingerprint("box_1")).toBeNull();

	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});
	expect(machines.getPinnedFingerprint("box_1")).toBe(CERT_FINGERPRINT);
	expect(machines.getPinnedFingerprint("no_such_machine")).toBeNull();
});

test("refresh() probes GET /api/v1/doctor with the passcode as a bearer credential and marks the machine online", async () => {
	const { fetchImpl, seenAuthorization } = doctorFetch("abc123XY");
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch: fetchImpl, probeTimeoutMs: 200 });
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});
	expect(machines.list()[0]).toMatchObject({ reachability: "unknown", lastSeen: null });

	const refreshed = await machines.refresh();

	expect(seenAuthorization).toContain("Bearer abc123XY");
	expect(refreshed[0]).toMatchObject({ id: "box_1", reachability: "online" });
	expect(refreshed[0].lastSeen).not.toBeNull();
	expect(machines.list()[0]).toMatchObject({ reachability: "online" });

	// The refreshed last-seen is persisted, so a relaunch still shows it.
	const reloaded = createPairedMachinesController({ stateDir, safeStorage });
	await reloaded.load();
	expect(reloaded.list()[0].lastSeen).toBe(refreshed[0].lastSeen);
});

test("refresh() reads agent-harness readiness from the doctor checks", async () => {
	const { fetchImpl } = doctorFetch("abc123XY", [
		{ level: "PASS", name: "claude-auth", section: "Agent harnesses", message: "signed in" },
	]);
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch: fetchImpl, probeTimeoutMs: 200 });
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const refreshed = await machines.refresh();
	expect(refreshed[0]).toMatchObject({ harness: "ready", harnessCommand: null });
});

test("refresh() marks an unreachable machine offline without bumping last-seen", async () => {
	const machines = controller(unreachableFetch);
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	const refreshed = await machines.refresh();
	expect(refreshed[0]).toMatchObject({ reachability: "offline", lastSeen: null });
});

test("refresh() marks online even on a 401 (wrong passcode): the box answered, only the credential is bad", async () => {
	const { fetchImpl } = doctorFetch("the-real-passcode");
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch: fetchImpl, probeTimeoutMs: 200 });
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "a-stale-passcode",
		fingerprint: CERT_FINGERPRINT,
	});

	const refreshed = await machines.refresh();
	expect(refreshed[0]).toMatchObject({ reachability: "online", harness: "unknown" });
});

test("the passcode never appears in the doctor probe's request path or in refresh()'s result", async () => {
	const { fetchImpl, requestedUrls } = doctorFetch("abc123XY");
	const machines = createPairedMachinesController({ stateDir, safeStorage, netFetch: fetchImpl, probeTimeoutMs: 200 });
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});
	const refreshed = await machines.refresh();

	expect(JSON.stringify(refreshed)).not.toContain("abc123XY");
	const doctorUrl = requestedUrls.find((url) => url.endsWith("/api/v1/doctor"));
	expect(doctorUrl).not.toContain("abc123XY");
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

test("load() migrates a v1 file on disk into v2 address hints", async () => {
	const v1File = {
		version: 1,
		machines: [
			{
				id: "box_1",
				name: "Pi",
				address: "192.168.1.5",
				port: 8443,
				pinnedFingerprint: CERT_FINGERPRINT,
				lastSeen: null,
				passcode: "enc:abc123XY",
			},
		],
	};
	await writeFile(path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME), JSON.stringify(v1File));

	const machines = controller();
	await machines.load();

	expect(machines.getAddresses("box_1")).toEqual(["192.168.1.5:8443"]);
	expect(machines.list()[0]).toMatchObject({ id: "box_1", baseUrl: "https://192.168.1.5:8443" });
});

test("load() drops a v1 record with an invalid port, the same as it always has", async () => {
	const v1File = {
		version: 1,
		machines: [
			{ id: "box_1", name: "Pi", address: "192.168.1.5", port: 99999, pinnedFingerprint: null, lastSeen: null, passcode: "enc:abc123XY" },
		],
	};
	await writeFile(path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME), JSON.stringify(v1File));

	const machines = controller();
	await machines.load();
	expect(machines.list()).toEqual([]);
});

test("loading a v1 file twice migrates identically (idempotent, since load() never writes)", async () => {
	const v1File = {
		version: 1,
		machines: [
			{ id: "box_1", name: "Pi", address: "192.168.1.5", port: 8443, pinnedFingerprint: null, lastSeen: null, passcode: "enc:abc123XY" },
		],
	};
	await writeFile(path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME), JSON.stringify(v1File));

	const machines = controller();
	await machines.load();
	const first = machines.getAddresses("box_1");
	await machines.load();
	const second = machines.getAddresses("box_1");
	expect(second).toEqual(first);
	expect(second).toEqual(["192.168.1.5:8443"]);
});

test("an unrecognized future schema version yields an empty registry, same as an unknown version always has", async () => {
	const futureFile = {
		version: 99,
		machines: [{ id: "box_1", name: "Pi", address: "192.168.1.5", port: 8443, pinnedFingerprint: null, lastSeen: null, passcode: "x" }],
	};
	await writeFile(path.join(stateDir, AO_PAIRED_MACHINES_FILE_NAME), JSON.stringify(futureFile));

	const machines = controller();
	await machines.load();
	expect(machines.list()).toEqual([]);
});

test("add with multiple hints persists them in order", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
		addresses: ["192.168.1.5:8443", "10.0.0.9:8443", "[fe80::1]:8443"],
	});

	expect(machines.getAddresses("box_1")).toEqual(["192.168.1.5:8443", "10.0.0.9:8443", "[fe80::1]:8443"]);

	const reloaded = createPairedMachinesController({ stateDir, safeStorage });
	await reloaded.load();
	expect(reloaded.getAddresses("box_1")).toEqual(["192.168.1.5:8443", "10.0.0.9:8443", "[fe80::1]:8443"]);
});

test("promoteAddress moves a hint to the front, updates the current address, and persists", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "192.168.1.5",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
		addresses: ["192.168.1.5:8443", "10.0.0.9:8443"],
	});

	await machines.promoteAddress("box_1", "10.0.0.9:8443");

	expect(machines.getAddresses("box_1")).toEqual(["10.0.0.9:8443", "192.168.1.5:8443"]);
	expect(machines.list()[0]).toMatchObject({ baseUrl: "https://10.0.0.9:8443" });

	const reloaded = createPairedMachinesController({ stateDir, safeStorage });
	await reloaded.load();
	expect(reloaded.getAddresses("box_1")).toEqual(["10.0.0.9:8443", "192.168.1.5:8443"]);
	expect(reloaded.list()[0]).toMatchObject({ baseUrl: "https://10.0.0.9:8443" });
});

test("promoteAddress accepts a hint not yet in the list, adding it at the front without dropping the others", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({ id: "box_1", name: "Pi", address: "192.168.1.5", port: 8443, passcode: "abc123XY", fingerprint: CERT_FINGERPRINT });

	await machines.promoteAddress("box_1", "10.0.0.9:8443");

	expect(machines.getAddresses("box_1")).toEqual(["10.0.0.9:8443", "192.168.1.5:8443"]);
	expect(machines.list()[0]).toMatchObject({ baseUrl: "https://10.0.0.9:8443" });
});

test("promoteAddress on an unregistered id is a no-op", async () => {
	const machines = controller();
	await machines.load();
	await expect(machines.promoteAddress("no_such_machine", "192.168.1.5:8443")).resolves.toBeUndefined();
});

test("promoteAddress rejects an unparseable hint", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({ id: "box_1", name: "Pi", address: "192.168.1.5", port: 8443, passcode: "abc123XY", fingerprint: CERT_FINGERPRINT });
	await expect(machines.promoteAddress("box_1", "not-a-hint")).rejects.toThrow(/usable address/);
});

test("add with an IPv6 address brackets it in baseUrl instead of throwing", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({
		id: "box_1",
		name: "Pi",
		address: "fd00::7",
		port: 8443,
		passcode: "abc123XY",
		fingerprint: CERT_FINGERPRINT,
	});

	expect(machines.list()[0]).toMatchObject({ baseUrl: "https://[fd00::7]:8443" });
});

test("promoteAddress promoting an IPv6 hint keeps the machine listed, bracketed in baseUrl", async () => {
	const machines = controller();
	await machines.load();
	await machines.add({ id: "box_1", name: "Pi", address: "192.168.1.5", port: 8443, passcode: "abc123XY", fingerprint: CERT_FINGERPRINT });

	await machines.promoteAddress("box_1", "[fd00::7]:8443");

	expect(machines.getAddresses("box_1")).toEqual(["[fd00::7]:8443", "192.168.1.5:8443"]);
	expect(machines.list()[0]).toMatchObject({ baseUrl: "https://[fd00::7]:8443" });
});
