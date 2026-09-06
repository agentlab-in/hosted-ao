import { describe, expect, it, vi } from "vitest";
vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn() }));
import { pairFromCode } from "./pairFlow";
import { encodePairingCode } from "./pairingCode";

const v1 = JSON.stringify({ v: 1, host: "192.168.1.42", port: 3011, password: "v1-password" });
const v2 = encodePairingCode({ v: 2, hostId: "h_expected", name: "", platform: "",
	endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }], token: "v2-token" });
const deps = () => ({ verify: vi.fn(async () => {}), persist: vi.fn(async () => {}) });

describe("frozen v1 pairing compatibility", () => {
	it("verifies the v1 supplied credential before persisting", async () => {
		const order: string[] = [];
		const d = { verify: vi.fn(async () => { order.push("verify"); }), persist: vi.fn(async () => { order.push("save"); }) };
		const result = await pairFromCode(v1, d);
		expect(result.ok).toBe(true);
		expect(order).toEqual(["verify", "save"]);
		expect(d.persist).toHaveBeenCalledWith(expect.objectContaining({ host: "192.168.1.42", password: "v1-password", secure: false }));
	});
	it("preserves pre-existing v1 TLS Tailscale pairing", async () => {
		const d = deps();
		await pairFromCode(JSON.stringify({ v: 1, host: "machine.tail123.ts.net", port: 443, password: "pw", secure: true }), d);
		expect(d.persist).toHaveBeenCalledWith(expect.objectContaining({ secure: true, httpPort: "443" }));
	});
	it("does not substitute a saved password when the v1 QR lacks one", async () => {
		const d = deps();
		expect(await pairFromCode(JSON.stringify({ v: 1, host: "192.168.1.42", port: 3011 }), d)).toEqual({ ok: false, reason: "auth" });
		expect(d.verify).not.toHaveBeenCalled(); expect(d.persist).not.toHaveBeenCalled();
	});
	it.each([401, 403, 429])("does not persist rejected credential status %s", async (status) => {
		const d = deps(); d.verify.mockRejectedValue({ status });
		expect(await pairFromCode(v1, d)).toEqual({ ok: false, reason: status === 429 ? "rate-limited" : "auth" });
		expect(d.persist).not.toHaveBeenCalled();
	});
	it.each([v2, `aomobile://pair#${v2}`, `https://agentlab.in/pair#${v2}`])("gates v2 scan/paste before verification or persistence", async (raw) => {
		const d = deps();
		expect(await pairFromCode(raw, d)).toEqual({ ok: false, reason: "v2-unavailable" });
		expect(d.verify).not.toHaveBeenCalled(); expect(d.persist).not.toHaveBeenCalled();
	});
	it("never tests or persists a mismatched v2 host identity", async () => {
		const d = deps();
		const wrong = encodePairingCode({ v: 2, hostId: "h_mismatch", name: "", platform: "",
			endpoints: [{ kind: "lan", host: "192.168.1.99", port: 3011, secure: false }], token: "other-token" });
		expect(await pairFromCode(wrong, d)).toEqual({ ok: false, reason: "v2-unavailable" });
		expect(d.verify).not.toHaveBeenCalled(); expect(d.persist).not.toHaveBeenCalled();
	});
	it("does not persist after cancellation during verification", async () => {
		const c = new AbortController(); const d = deps();
		d.verify.mockImplementation(async () => { c.abort(); });
		expect(await pairFromCode(v1, d, c.signal)).toEqual({ ok: false, reason: "cancelled" });
		expect(d.persist).not.toHaveBeenCalled();
	});
	it("does not start verification when already cancelled", async () => {
		const c = new AbortController(); c.abort(); const d = deps();
		expect(await pairFromCode(v1, d, c.signal)).toEqual({ ok: false, reason: "cancelled" });
		expect(d.verify).not.toHaveBeenCalled();
	});
	it("reports malformed input without persisting", async () => {
		const d = deps(); expect(await pairFromCode("not a code", d)).toEqual({ ok: false, reason: "not-ao-qr" });
		expect(d.persist).not.toHaveBeenCalled();
	});
});
