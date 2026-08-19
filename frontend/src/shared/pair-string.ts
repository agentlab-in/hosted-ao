// Parser for the ao-pair:// pairing string, the single credential a box
// prints and the desktop app pastes to add a machine. Twin of
// backend/internal/pairstring (Go); both sides are tested against the same
// golden vectors (backend/internal/pairstring/vectors.json) so the grammar
// never drifts between the CLI/box side and this desktop app side. See
// docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md.
//
//	ao-pair://v1/<addr>[,<addr>...]#<fp>:<passcode>
//
//	addr     = host:port, host is IPv4, a bracketed IPv6 literal, or a DNS
//	           name; port is required and explicit, 1-65535
//	fp       = 64 lowercase hex chars, the SHA-256 of the gateway cert's DER
//	passcode = 8 chars, [A-Za-z0-9]
//
// Parsing is manual (no URL()): URL() normalizes hosts, percent-decodes,
// and silently accepts things this grammar must reject (userinfo, query
// strings, extra fragments), so it would hide exactly the malformations
// this parser exists to catch.
//
// No Electron imports: this module is pure and loaded from both the
// renderer and the main process, like every other frontend/src/shared
// module. It never logs the raw string or the passcode.

export interface PairAddr {
	host: string;
	port: number;
}

export interface ParsedPairString {
	addrs: PairAddr[];
	fingerprintHex: string;
	passcode: string;
}

export interface PairStringError {
	error: string;
}

const SCHEME = "ao-pair://";
const VERSION_SEGMENT = "v1/";
const FINGERPRINT_LEN = 64;
const PASSCODE_LEN = 8;

/** Parse and validate a full pairing string against the grammar. Address
 * order is preserved exactly as given; this never reorders. */
export function parsePairString(raw: string): ParsedPairString | PairStringError {
	if (!raw.startsWith(SCHEME)) {
		return { error: "missing ao-pair:// scheme" };
	}
	let rest = raw.slice(SCHEME.length);

	if (!rest.startsWith(VERSION_SEGMENT)) {
		return { error: "version segment must be v1" };
	}
	rest = rest.slice(VERSION_SEGMENT.length);

	const hashCount = countChar(rest, "#");
	if (hashCount === 0) {
		return { error: "missing '#' fragment separator" };
	}
	if (hashCount > 1) {
		return { error: "only one fragment separator is permitted" };
	}

	const hashIdx = rest.indexOf("#");
	const addrPart = rest.slice(0, hashIdx);
	const tail = rest.slice(hashIdx + 1);

	if (addrPart === "") {
		return { error: "at least one address is required" };
	}

	const addrs: PairAddr[] = [];
	for (const addrStr of addrPart.split(",")) {
		const parsed = parseAddr(addrStr);
		if ("error" in parsed) return parsed;
		addrs.push(parsed);
	}

	if (tail.includes("?")) {
		return { error: "no query string is permitted" };
	}

	// Split on the LAST ':' rather than requiring exactly one: a fingerprint
	// that erroneously contains a colon (invalid, but must be reported as a
	// fingerprint problem, not misparsed as a wrong-length passcode) still
	// leaves the real fp/passcode separator as the last colon in the tail,
	// since a valid passcode never contains one.
	const lastColon = tail.lastIndexOf(":");
	if (lastColon === -1) {
		return { error: "expected a ':' between fingerprint and passcode" };
	}
	const fingerprintHex = tail.slice(0, lastColon);
	const passcode = tail.slice(lastColon + 1);

	const fpError = validateFingerprint(fingerprintHex);
	if (fpError) return { error: fpError };

	const passcodeError = validatePasscode(passcode);
	if (passcodeError) return { error: passcodeError };

	return { addrs, fingerprintHex, passcode };
}

/** Render a 64-char lowercase hex fingerprint (the fp field this module
 * parses) in the exact format `paired-machines.ts` persists as
 * `pinnedFingerprint`: uppercase hex octets joined by colons. That value
 * comes from `computePairFingerprint` (frontend/src/main/paired-machine-cert.ts),
 * which is Electron's `X509Certificate#fingerprint256` -- the same
 * rendering as backend/internal/vmgateway/paircert.go's `PairFingerprint`,
 * e.g. "07:CA:9F:3E:...". */
export function toPinnedFingerprintFormat(fpHex: string): string {
	const octets: string[] = [];
	for (let i = 0; i < fpHex.length; i += 2) {
		octets.push(fpHex.slice(i, i + 2).toUpperCase());
	}
	return octets.join(":");
}

function countChar(s: string, ch: string): number {
	let count = 0;
	for (const c of s) {
		if (c === ch) count++;
	}
	return count;
}

function parseAddr(addr: string): PairAddr | PairStringError {
	if (addr.includes("@")) {
		return { error: "address must not include a username" };
	}

	let host: string;
	let portStr: string;
	if (addr.startsWith("[")) {
		const closeIdx = addr.indexOf("]");
		if (closeIdx === -1 || addr[closeIdx + 1] !== ":") {
			return { error: "address must include an explicit port" };
		}
		host = addr.slice(1, closeIdx);
		portStr = addr.slice(closeIdx + 2);
	} else {
		const colonIdx = addr.lastIndexOf(":");
		if (colonIdx === -1) {
			return { error: "address must include an explicit port" };
		}
		host = addr.slice(0, colonIdx);
		portStr = addr.slice(colonIdx + 1);
		// A second colon in an unbracketed host is an IPv6 literal missing its
		// required brackets, not a valid host:port split.
		if (host.includes(":")) {
			return { error: "address must include an explicit port" };
		}
	}

	if (host === "" || portStr === "" || !/^\d+$/.test(portStr)) {
		return { error: "address must include an explicit port" };
	}
	const port = Number(portStr);
	if (port < 1 || port > 65535) {
		return { error: "port must be between 1 and 65535" };
	}
	return { host, port };
}

function validateFingerprint(fp: string): string | null {
	if (fp.includes(":")) return "fingerprint must not contain colons";
	if (fp.length !== FINGERPRINT_LEN) return "fingerprint must be exactly 64 hex characters";
	if (!/^[0-9a-f]+$/.test(fp)) return "fingerprint must be lowercase hex";
	return null;
}

function validatePasscode(p: string): string | null {
	if (p.length !== PASSCODE_LEN) return "passcode must be exactly 8 characters";
	if (!/^[A-Za-z0-9]+$/.test(p)) return "passcode must be alphanumeric";
	return null;
}
