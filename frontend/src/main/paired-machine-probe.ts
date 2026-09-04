import { isIP } from "node:net";
import { connect, type TLSSocket } from "node:tls";
import { computePairFingerprint } from "./paired-machine-cert";

export type PairFingerprintProbeOptions = {
	timeoutMs: number;
	signal?: AbortSignal;
};

export type PairFingerprintProbe = (
	address: string,
	port: number,
	options: PairFingerprintProbeOptions,
) => Promise<string>;

/**
 * Canonical hostname shared by the raw TLS probe and Electron's certificate
 * verifier. URL.hostname includes brackets around IPv6 literals in Node, while
 * pairing records store them bare; returning the bare form keeps both paths on
 * one key. URL parsing also lower-cases DNS names for us.
 */
export function canonicalPairHost(address: string): string | null {
	const trimmed = address.trim();
	if (!trimmed) return null;
	const unbracketed = trimmed.startsWith("[") && trimmed.endsWith("]") ? trimmed.slice(1, -1) : trimmed;
	const candidate = isIP(unbracketed) === 6 ? `[${unbracketed}]` : unbracketed;
	try {
		const url = new URL(`https://${candidate}`);
		if (url.username || url.password || url.port || url.pathname !== "/" || url.search || url.hash) return null;
		const hostname = url.hostname;
		return hostname.startsWith("[") && hostname.endsWith("]")
			? hostname.slice(1, -1).toLowerCase()
			: hostname.toLowerCase();
	} catch {
		return null;
	}
}

/**
 * Capture the leaf certificate presented by a pair-mode gateway without
 * issuing an HTTP request or involving Chromium's network stack. The socket is
 * intentionally allowed to complete a handshake with a self-signed peer only
 * long enough to read its certificate; no application data or credentials are
 * sent, and the caller still has to compare and explicitly pin the returned
 * fingerprint before normal Electron traffic can be accepted.
 */
export const capturePairFingerprint: PairFingerprintProbe = (address, port, options) => {
	const host = canonicalPairHost(address);
	if (!host) return Promise.reject(new Error(`Not a usable address: ${address}`));
	if (!Number.isInteger(port) || port < 1 || port > 65535) {
		return Promise.reject(new Error(`Not a valid port: ${port}`));
	}
	if (!Number.isFinite(options.timeoutMs) || options.timeoutMs <= 0) {
		return Promise.reject(new Error("Pairing probe timeout must be positive."));
	}
	if (options.signal?.aborted) return Promise.reject(new Error("Pairing probe was cancelled."));

	return new Promise<string>((resolve, reject) => {
		let settled = false;
		let socket: TLSSocket;
		let timer: ReturnType<typeof setTimeout> | undefined;

		const finish = (settle: () => void) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			options.signal?.removeEventListener("abort", onAbort);
			socket.removeListener("secureConnect", onSecureConnect);
			socket.removeListener("error", onError);
			socket.destroy();
			settle();
		};
		const onAbort = () => finish(() => reject(new Error("Pairing probe was cancelled.")));
		const onError = (err: Error) => finish(() => reject(err));
		const onSecureConnect = () => {
			const certificate = socket.getPeerCertificate();
			if (!certificate.raw) {
				finish(() => reject(new Error("The peer did not present a certificate.")));
				return;
			}
			let fingerprint: string;
			try {
				fingerprint = computePairFingerprint(certificate.raw);
			} catch (err) {
				finish(() => reject(err));
				return;
			}
			finish(() => resolve(fingerprint));
		};

		socket = connect({
			host,
			port,
			rejectUnauthorized: false,
			...(isIP(host) === 0 ? { servername: host } : {}),
		});
		socket.once("secureConnect", onSecureConnect);
		socket.once("error", onError);
		options.signal?.addEventListener("abort", onAbort, { once: true });
		if (options.signal?.aborted) {
			onAbort();
			return;
		}
		timer = setTimeout(
			() => finish(() => reject(new Error(`Pairing probe timed out after ${Math.round(options.timeoutMs / 1000)}s.`))),
			options.timeoutMs,
		);
	});
};
