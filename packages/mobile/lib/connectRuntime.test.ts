import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: (...args: Parameters<typeof fetch>) => globalThis.fetch(...args) }));
import { DEFAULT_CONFIG } from "./config";
import { connectToHost, probeEndpoint, probeIdentity, PROBE_TIMEOUT_MS, runtimeConnectDeps, verifyLegacyConnection } from "./connectRuntime";
import { pairFromCode } from "./pairFlow";
import { encodePairingCode } from "./pairingCode";
import { resolveActiveConfig } from "./resolveConfig";
import type { Endpoint } from "./endpoints";
const lan: Endpoint = { kind: "lan", host: "192.168.1.42", port: 3011, secure: false };
const config = { ...DEFAULT_CONFIG, host: lan.host, password: "expected-v1-password" };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	vi.useFakeTimers();
	fetchMock = vi.fn(async (_url: string, init: RequestInit) => response({ sessions: [] },
		(init.headers as Record<string, string>).Authorization === `Bearer ${config.password}` ? 200 : 401));
	vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("authenticated v1 transport with v2 unavailable", () => {
	it("pairs v1 through the real authenticated verifier before saving", async () => {
		const save = vi.fn(async () => { expect(fetchMock).toHaveBeenCalledTimes(1); });
		const result = await pairFromCode(JSON.stringify({ v: 1, host: lan.host, port: lan.port, password: config.password }),
			{ verify: verifyLegacyConnection, persist: save });
		expect(result.ok).toBe(true); expect(save).toHaveBeenCalledOnce();
		expect(fetchMock).toHaveBeenCalledWith(`http://${lan.host}:3011/api/v1/sessions`, expect.objectContaining({
			headers: { Authorization: `Bearer ${config.password}` }, redirect: "error", credentials: "omit",
		}));
		expect(vi.getTimerCount()).toBe(0);
	});
	it("reconnects the stored v1 config without identity or endpoint requests", async () => {
		expect(await resolveActiveConfig({ loadLegacyConfig: async () => config, verify: verifyLegacyConnection })).toEqual(config);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(fetchMock.mock.calls[0][0]).toContain("/api/v1/sessions");
	});
	it("preserves manual password and TLS Tailscale transport", async () => {
		await verifyLegacyConnection({ ...config, host: "machine.tail123.ts.net", httpPort: "443", secure: true });
		expect(fetchMock.mock.calls[0][0]).toBe("https://machine.tail123.ts.net:443/api/v1/sessions");
		expect(fetchMock.mock.calls[0][1].headers).toEqual({ Authorization: `Bearer ${config.password}` });
	});
	it("fails missing credentials before fetch", async () => {
		await expect(verifyLegacyConnection({ ...config, password: "" })).rejects.toMatchObject({ status: 401 });
		expect(fetchMock).not.toHaveBeenCalled();
	});
	it("fails a wrong credential without retry or persistence", async () => {
		const save = vi.fn();
		const result = await pairFromCode(JSON.stringify({ v: 1, host: lan.host, port: lan.port, password: "wrong" }),
			{ verify: verifyLegacyConnection, persist: save });
		expect(result).toEqual({ ok: false, reason: "auth" }); expect(save).not.toHaveBeenCalled();
		expect(fetchMock).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0);
	});
	it.each(["h_expected", "h_mismatch"])("gates v2 identity %s with zero token release and no save", async (hostId) => {
		const save = vi.fn();
		const code = encodePairingCode({ v: 2, hostId, name: "", platform: "", endpoints: [lan], token: "v2-secret" });
		expect(await pairFromCode(`aomobile://pair#${code}`, { verify: verifyLegacyConnection, persist: save }))
			.toEqual({ ok: false, reason: "v2-unavailable" });
		expect(fetchMock).not.toHaveBeenCalled(); expect(save).not.toHaveBeenCalled();
	});
	it("never activates stored v2 candidates or anonymous identity entrypoints", async () => {
		await expect(probeEndpoint(lan, new AbortController().signal)).rejects.toThrow("unavailable");
		await expect(probeIdentity(config)).rejects.toThrow("unavailable");
		expect((await connectToHost("h_v2")).ok).toBe(false);
		expect((await runtimeConnectDeps().race({ id: "h_v2", name: "", platform: "", endpoints: [lan], token: "v2-token", lastConnected: 1 })).ok).toBe(false);
		expect(fetchMock).not.toHaveBeenCalled();
	});
	it("rejects v2 metadata before authenticated requests", async () => {
		await expect(verifyLegacyConnection({ ...config, hostId: "h_v2" })).rejects.toThrow("unavailable");
		await expect(verifyLegacyConnection({ ...config, endpointKind: "lan" })).rejects.toThrow("unavailable");
		expect(fetchMock).not.toHaveBeenCalled();
	});
	it.each(["outside.example", "foo.trycloudflare.com", "good@evil.test", "192.168.1.42/path"])("rejects an out-of-scope authority %s", async (host) => {
		await expect(verifyLegacyConnection({ ...config, host })).rejects.toThrow();
		expect(fetchMock).not.toHaveBeenCalled();
	});
	it("cancels in-flight requests and supports a clean retry", async () => {
		fetchMock.mockImplementationOnce((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
			init.signal?.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
		}));
		const c = new AbortController(); const remove = vi.spyOn(c.signal, "removeEventListener");
		const pending = verifyLegacyConnection(config, c.signal); const check = expect(pending).rejects.toThrow("aborted");
		c.abort(); await check;
		expect(remove).toHaveBeenCalledWith("abort", expect.any(Function)); expect(vi.getTimerCount()).toBe(0);
		await verifyLegacyConnection(config); expect(fetchMock).toHaveBeenCalledTimes(2);
	});
	it("times out stalled requests and releases resources", async () => {
		fetchMock.mockImplementationOnce((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
			init.signal?.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
		}));
		const check = expect(verifyLegacyConnection(config)).rejects.toThrow("aborted");
		await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS); await check; expect(vi.getTimerCount()).toBe(0);
	});
	it("does not start requests after cancellation", async () => {
		const c = new AbortController(); c.abort();
		await expect(verifyLegacyConnection(config, c.signal)).rejects.toThrow("cancelled");
		expect(fetchMock).not.toHaveBeenCalled();
	});
	it("redacts rejected response bodies and emits no credential logs", async () => {
		const logs = [vi.spyOn(console, "log"), vi.spyOn(console, "warn"), vi.spyOn(console, "error")];
		try {
			fetchMock.mockResolvedValue(response({ message: config.password }, 401));
			await expect(verifyLegacyConnection(config)).rejects.toThrow("Connection request returned 401");
			for (const log of logs) expect(log).not.toHaveBeenCalled();
			expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true);
		} finally { for (const log of logs) log.mockRestore(); }
	});
});
