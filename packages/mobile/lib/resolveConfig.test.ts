import { describe, expect, it, vi } from "vitest";
vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));
import { DEFAULT_CONFIG } from "./config";
import { resolveActiveConfig } from "./resolveConfig";
const config = { ...DEFAULT_CONFIG, host: "192.168.1.42", password: "v1-password" };
const deps = () => ({ loadLegacyConfig: vi.fn(async () => config), verify: vi.fn(async () => {}) });

describe("v1 reconnect without upstream identity racing", () => {
	it("verifies and returns the existing single-server configuration", async () => {
		const d = deps(); expect(await resolveActiveConfig(d)).toEqual(config);
		expect(d.verify).toHaveBeenCalledWith(config, undefined);
	});
	it.each([{ hostId: "h_v2" }, { endpointKind: "lan" as const }])("does not verify v2-derived configs: %j", async (metadata) => {
		const d = deps(); d.loadLegacyConfig.mockResolvedValue({ ...config, ...metadata });
		expect(await resolveActiveConfig(d)).toBeNull(); expect(d.verify).not.toHaveBeenCalled();
	});
	it("does not verify missing credentials", async () => {
		const d = deps(); d.loadLegacyConfig.mockResolvedValue({ ...config, password: "" });
		expect(await resolveActiveConfig(d)).toBeNull(); expect(d.verify).not.toHaveBeenCalled();
	});
	it("does not fall back around failed authentication", async () => {
		const d = deps(); d.verify.mockRejectedValue({ status: 401 });
		expect(await resolveActiveConfig(d)).toBeNull();
	});
	it("cancellation during verification does not return a connected config", async () => {
		const c = new AbortController(); const d = deps(); d.verify.mockImplementation(async () => { c.abort(); });
		expect(await resolveActiveConfig(d, c.signal)).toBeNull();
	});
});
