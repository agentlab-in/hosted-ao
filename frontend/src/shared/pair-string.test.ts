import { expect, test } from "vitest";
import vectors from "../../../backend/internal/pairstring/vectors.json";
import { parsePairString, toPinnedFingerprintFormat } from "./pair-string";

// vectors.json is the golden-vector contract this module shares with
// backend/internal/pairstring (Go). Do not fork or copy it; import it in
// place so both sides of the codec stay honest against the same cases.

for (const vector of vectors.valid) {
	test(`parses valid vector: ${vector.name}`, () => {
		const result = parsePairString(vector.string);
		expect("error" in result).toBe(false);
		if ("error" in result) return; // narrow for TS after the assertion above
		expect(result.addrs).toEqual(
			vector.addrs.map((addr) => {
				const closeBracket = addr.lastIndexOf("]");
				const portSep = addr.lastIndexOf(":");
				const host = addr.startsWith("[") ? addr.slice(1, closeBracket) : addr.slice(0, portSep);
				const port = Number(addr.slice(portSep + 1));
				return { host, port };
			}),
		);
		expect(result.fingerprintHex).toBe(vector.fp);
		expect(result.passcode).toBe(vector.passcode);
	});
}

for (const vector of vectors.invalid) {
	test(`rejects invalid vector: ${vector.name}`, () => {
		const result = parsePairString(vector.string);
		expect("error" in result).toBe(true);
		if (!("error" in result)) return;
		expect(result.error).toBe(vector.reason);
	});
}

test("preserves address order as given, never reorders", () => {
	const result = parsePairString(
		"ao-pair://v1/203.0.113.7:8443,192.168.1.40:8443#" + "ab".repeat(32) + ":XK4M2P7Q",
	);
	expect("error" in result).toBe(false);
	if ("error" in result) return;
	expect(result.addrs).toEqual([
		{ host: "203.0.113.7", port: 8443 },
		{ host: "192.168.1.40", port: 8443 },
	]);
});

// toPinnedFingerprintFormat must match what paired-machines.ts persists as
// pinnedFingerprint: computePairFingerprint (paired-machine-cert.ts) returns
// Electron's X509Certificate#fingerprint256, which is uppercase hex octets
// joined by colons (the same rendering as backend/internal/vmgateway/paircert.go's
// PairFingerprint, e.g. "07:CA:9F:3E:...").
test("toPinnedFingerprintFormat matches the pin store's colon-separated uppercase hex format", () => {
	const fpHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
	expect(fpHex).toHaveLength(64);
	expect(toPinnedFingerprintFormat(fpHex)).toBe(
		"01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF",
	);
});

test("toPinnedFingerprintFormat round-trips a golden vector's fingerprint", () => {
	const vector = vectors.valid[0];
	const formatted = toPinnedFingerprintFormat(vector.fp);
	expect(formatted).toMatch(/^([0-9A-F]{2}:){31}[0-9A-F]{2}$/);
	expect(formatted.replace(/:/g, "").toLowerCase()).toBe(vector.fp);
});
