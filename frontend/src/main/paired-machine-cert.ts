import { X509Certificate } from "node:crypto";

/**
 * Certificate-pin enforcement for paired boxes, at the Electron session level.
 *
 * A paired machine (docs/adr/0003-pair-mode-gateway.md) has no certificate
 * authority behind it: the box mints a long-lived self-signed certificate and
 * the desktop pins its fingerprint on first connect (trust-on-first-use). That
 * pin has to be enforced everywhere the session talks to the box, not only on
 * REST calls, because the terminal mux WebSocket and the SSE event stream ride
 * the same TLS connection. `session.setCertificateVerifyProc` is the one seam
 * that sees every connection a session makes regardless of which API opened
 * it, which is why the verifier here is wired directly into
 * `session.defaultSession` in main.ts rather than into a fetch wrapper.
 *
 * The corollary is the hard constraint this module exists to satisfy: it must
 * touch nothing about how any other host is verified. `isPairHost` is the
 * gate, and a request for a hostname it does not recognise is handed back to
 * Chromium's own verification untouched (CERT_VERIFY_USE_DEFAULT).
 */

/** callback(0): accept the certificate for this connection. */
export const CERT_VERIFY_ACCEPT = 0;
/** callback(-2): reject outright. No click-through exists above this module. */
export const CERT_VERIFY_REJECT = -2;
/** callback(-3): defer to Chromium's own verification result, unmodified. */
export const CERT_VERIFY_USE_DEFAULT = -3;

/**
 * The subset of Electron's `Request` (see `session.setCertificateVerifyProc`)
 * this module reads. Kept minimal and structural, rather than importing
 * `Electron.Request`, so this file has no runtime dependency on the `electron`
 * module and unit tests can construct one with a plain object.
 */
export type PairCertificateRequest = {
	/** Host being connected to. Electron does not report the port here, so a
	 * pin is scoped to a hostname, not a hostname:port pair. */
	hostname: string;
	certificate: { data: string };
};

export type PairCertificateVerifyProc = (
	request: PairCertificateRequest,
	callback: (verificationResult: number) => void,
) => void;

/** What the verifier needs from the paired-machine registry, kept in memory so
 * every call is synchronous: Electron calls this proc from the network
 * service and does not await it. */
export type PairPinLookup = {
	/** True for a registered paired machine, or a host currently being probed
	 * for first-time pairing. False for every other host, including the
	 * control plane and any hosted machine with a real certificate. */
	isPairHost: (hostname: string) => boolean;
	/** The pinned fingerprint for a registered paired machine, or null when
	 * the host is only a pairing candidate with nothing pinned yet. */
	getPinnedFingerprint: (hostname: string) => string | null;
	/** Called with the fingerprint of whatever certificate was actually
	 * presented, for every pair-host connection, whether accepted or
	 * rejected. This is the first-connect capture: the pairing flow reads it
	 * back to show the user what to compare against the box's printout. */
	onPresented: (hostname: string, fingerprint: string) => void;
};

/**
 * Render a certificate's SHA-256 fingerprint the same way
 * `backend/internal/vmgateway/paircert.go`'s `PairFingerprint` does: uppercase
 * hex octets joined by colons, over the leaf certificate's DER bytes.
 *
 * `X509Certificate.fingerprint256` computes exactly that (SHA-256 over the
 * certificate's DER encoding, rendered as upper-case colon-separated hex) for
 * both PEM and DER input, so this is the whole implementation; hand-rolling a
 * PEM parse and hasher would only be a slower way to arrive at the same
 * bytes. `pem` is `Certificate.data` from Electron's verify-proc request,
 * which is PEM encoded.
 */
export function computePairFingerprint(pem: string): string {
	return new X509Certificate(pem).fingerprint256;
}

/**
 * Build the proc to hand to `session.setCertificateVerifyProc`.
 *
 * Decision table, in order:
 * 1. Not a pair host at all -> CERT_VERIFY_USE_DEFAULT. Every other host,
 *    including the control plane and hosted machines, is untouched.
 * 2. A pair host with a pinned fingerprint that matches what was presented ->
 *    CERT_VERIFY_ACCEPT.
 * 3. Anything else for a pair host -- a mismatch, or no pin yet -> REJECT.
 *    There is no accept path for an unpinned host: the presented fingerprint
 *    is still captured via `onPresented` so the pairing flow can show it, but
 *    the connection itself is refused rather than silently trusted. This is
 *    what makes trust-on-first-use a *user* accepting a fingerprint (task 8's
 *    UI, by calling the registry's add/pin step) rather than the transport
 *    accepting it on the user's behalf.
 */
export function createPairCertificateVerifyProc(lookup: PairPinLookup): PairCertificateVerifyProc {
	return (request, callback) => {
		if (!lookup.isPairHost(request.hostname)) {
			callback(CERT_VERIFY_USE_DEFAULT);
			return;
		}
		const presented = computePairFingerprint(request.certificate.data);
		lookup.onPresented(request.hostname, presented);
		const pinned = lookup.getPinnedFingerprint(request.hostname);
		callback(pinned && pinned === presented ? CERT_VERIFY_ACCEPT : CERT_VERIFY_REJECT);
	};
}
