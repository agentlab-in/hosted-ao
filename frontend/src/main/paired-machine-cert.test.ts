// @vitest-environment node
import { expect, test, vi } from "vitest";
import {
	CERT_VERIFY_ACCEPT,
	CERT_VERIFY_REJECT,
	CERT_VERIFY_USE_DEFAULT,
	computePairFingerprint,
	createPairCertificateVerifyProc,
	type PairCertificateRequest,
} from "./paired-machine-cert";

/**
 * A real self-signed EC certificate (generated once with `openssl req -x509
 * -newkey ec ...`, CN=test), used as a fixture rather than generated at test
 * time so the expected fingerprint below is a fixed, independently
 * verifiable value, not something the test computes and then checks against
 * itself.
 */
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

/**
 * The SHA-256 fingerprint of CERT_PEM's DER bytes, uppercase hex octets
 * joined by colons. Independently verified with:
 *   openssl x509 -in cert.pem -noout -fingerprint -sha256
 * and hand-computed with sha256(DER) + `%02X` joined by `:`, matching Go's
 * PairFingerprint exactly (backend/internal/vmgateway/paircert.go) byte for
 * byte -- this is the same rendering, not a coincidentally similar one.
 */
const CERT_FINGERPRINT = "DF:9A:6C:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";

/** A second, unrelated certificate, standing in for a swapped/attacker cert. */
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

function request(hostname: string, pem: string): PairCertificateRequest {
	return { hostname, certificate: { data: pem } };
}

test("computePairFingerprint matches PairFingerprint's exact rendering: uppercase hex octets joined by colons", () => {
	const fingerprint = computePairFingerprint(CERT_PEM);
	expect(fingerprint).toBe(CERT_FINGERPRINT);
	expect(fingerprint).toMatch(/^([0-9A-F]{2}:){31}[0-9A-F]{2}$/);
});

test("a host that is not a paired machine gets default Chromium verification, untouched", () => {
	const getPinnedFingerprint = vi.fn(() => null);
	const proc = createPairCertificateVerifyProc({ getPinnedFingerprint });

	const callback = vi.fn();
	proc(request("ao.agentlab.in", CERT_PEM), callback);

	expect(callback).toHaveBeenCalledExactlyOnceWith(CERT_VERIFY_USE_DEFAULT);
	expect(getPinnedFingerprint).toHaveBeenCalledExactlyOnceWith("ao.agentlab.in");
});

test("a pinned machine whose presented certificate matches the pin is accepted", () => {
	const proc = createPairCertificateVerifyProc({
		getPinnedFingerprint: () => CERT_FINGERPRINT,
	});

	const callback = vi.fn();
	proc(request("192.168.1.5", CERT_PEM), callback);

	expect(callback).toHaveBeenCalledExactlyOnceWith(CERT_VERIFY_ACCEPT);
});

test("a pinned machine whose presented certificate does not match the pin is refused, with no fallback", () => {
	const proc = createPairCertificateVerifyProc({
		getPinnedFingerprint: () => CERT_FINGERPRINT,
	});

	const callback = vi.fn();
	// OTHER_CERT_PEM stands in for a swapped or attacker certificate: the
	// hostname is recognised as a paired machine, but what is presented does
	// not match what was pinned.
	proc(request("192.168.1.5", OTHER_CERT_PEM), callback);

	expect(callback).toHaveBeenCalledExactlyOnceWith(CERT_VERIFY_REJECT);
	// No other verification result was ever offered: not the default result,
	// and not an accept. A hard refusal has exactly one outcome.
	expect(callback).not.toHaveBeenCalledWith(CERT_VERIFY_ACCEPT);
	expect(callback).not.toHaveBeenCalledWith(CERT_VERIFY_USE_DEFAULT);
});

test("first connect is left to Chromium because raw TLS capture has not pinned the host yet", () => {
	const proc = createPairCertificateVerifyProc({
		getPinnedFingerprint: () => null,
	});

	const callback = vi.fn();
	proc(request("192.168.1.5", CERT_PEM), callback);

	// Chromium still rejects the self-signed certificate by default; the
	// verifier itself neither accepts nor creates trust before `add()` pins it.
	expect(callback).toHaveBeenCalledExactlyOnceWith(CERT_VERIFY_USE_DEFAULT);
});
