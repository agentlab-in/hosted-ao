import { beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";
import { DEFAULT_CONFIG, loadConfig } from "./config";
beforeEach(() => { vi.clearAllMocks(); });

describe("saved mobile config compatibility gate", () => {
	it("loads frozen v1 storage without changing its credential format", async () => {
		vi.mocked(AsyncStorage.getItem).mockResolvedValue(JSON.stringify({ host: "192.168.1.42", httpPort: "3011", secure: false }));
		vi.mocked(SecureStore.getItemAsync).mockResolvedValue("v1-password");
		expect(await loadConfig()).toMatchObject({ host: "192.168.1.42", password: "v1-password", secure: false });
		expect(SecureStore.setItemAsync).not.toHaveBeenCalled();
	});
	it.each([{ hostId: "h_v2" }, { hostId: "" }, { endpointKind: "lan" }, { endpointKind: "tunnel" }])(
		"never exposes v2 credentials to direct terminal/settings fallback reads: %j", async (metadata) => {
			vi.mocked(AsyncStorage.getItem).mockResolvedValue(JSON.stringify({ host: "192.168.1.99", password: "v2-secret", ...metadata }));
			expect(await loadConfig()).toEqual(DEFAULT_CONFIG);
			expect(SecureStore.getItemAsync).not.toHaveBeenCalled();
			expect(SecureStore.setItemAsync).not.toHaveBeenCalled();
			expect(AsyncStorage.setItem).not.toHaveBeenCalled();
		});
});
