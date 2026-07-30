/**
 * The machines the desktop app can point itself at: this computer, plus the
 * machines the signed-in account has registered with the control plane.
 *
 * The registered ones come from `GET /api/v1/machines`, which accepts only a
 * control-plane-audience access token (see controlplane/TOKEN_CONTRACT.md,
 * "The two audiences"). This module is the pure part: shapes and parsing, no
 * I/O, so the main process owns the fetch and the renderer owns the rendering.
 */

/** Machine zero. Never comes from the control plane and never needs an account. */
export const LOCAL_MACHINE_ID = "local";

/** What a user runs on the machine itself when no agent harness is set up. */
export const HARNESS_SETUP_COMMAND = "ao vm setup-harness claude";

/** Whether the machine answered a liveness probe on this refresh. */
export type AoMachineReachability = "unknown" | "online" | "offline";

/** Whether an agent harness is set up on the machine. */
export type AoMachineHarness = "ready" | "missing" | "unknown";

export type AoMachine = {
	id: string;
	name: string;
	/** HTTPS origin of the machine's gateway, with no trailing slash. Empty for the local machine. */
	baseUrl: string;
	/** True only for machine zero, which is this computer. */
	local: boolean;
	/** ISO 8601, or null when the control plane did not report one. */
	createdAt: string | null;
	/** ISO 8601 of the last contact the control plane recorded, or null for never. */
	lastSeen: string | null;
	reachability: AoMachineReachability;
	harness: AoMachineHarness;
	/** The exact command to run on the machine. Set when `harness` is "missing". */
	harnessCommand: string | null;
};

export type AoMachinesStatus =
	// No account, so there is nothing to list. The local machine is still offered.
	| "signed-out"
	| "loading"
	| "ready"
	// The list could not be fetched. `error` says why; the local machine still works.
	| "error";

export type AoMachinesState = {
	status: AoMachinesStatus;
	/** Always non-empty: the local machine is index zero, signed in or not. */
	machines: AoMachine[];
	activeMachineId: string;
	/** Last failure listing machines. Never contains a token. */
	error?: string;
};

/** Machine zero, always present. Local use never requires an account. */
export function localMachine(name: string): AoMachine {
	return {
		id: LOCAL_MACHINE_ID,
		name,
		baseUrl: "",
		local: true,
		createdAt: null,
		lastSeen: null,
		// This computer is reachable by definition: the app is running on it.
		reachability: "online",
		// Local harness state is already covered by `ao doctor` on this machine
		// and by the agent pickers, so the machine list does not restate it.
		harness: "unknown",
		harnessCommand: null,
	};
}

/**
 * Normalize a machine's public URL to a bare origin.
 *
 * The control plane already stores it normalized (its `normalizePublicURL`), so
 * this mostly guards against a row that predates that or was written by hand: a
 * value with a path, credentials, or a scheme the renderer cannot talk to is
 * rejected rather than turned into a base URL the app would then point at.
 */
export function parseMachineOrigin(raw: unknown): string | null {
	if (typeof raw !== "string" || !raw.trim()) return null;
	const candidate = raw.trim();
	let url: URL;
	try {
		url = new URL(candidate.includes("://") ? candidate : `https://${candidate}`);
	} catch {
		return null;
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") return null;
	if (url.pathname !== "/" || url.search || url.hash || url.username || url.password) return null;
	return url.origin;
}

function optionalIso(raw: unknown): string | null {
	if (typeof raw !== "string" || !raw.trim()) return null;
	return Number.isNaN(Date.parse(raw)) ? null : raw;
}

/** One check from `ao doctor --json` (backend/internal/cli/doctor.go). */
export type DoctorCheck = {
	level?: unknown;
	section?: unknown;
	name?: unknown;
	message?: unknown;
	/** The one command that fixes this check, when a single command does. */
	remediation?: unknown;
};

/** The check that answers whether an agent harness is set up on a machine. */
export const HARNESS_DOCTOR_CHECK = "claude-auth";

type HarnessReadiness = Pick<AoMachine, "harness" | "harnessCommand">;

const HARNESS_UNKNOWN: HarnessReadiness = { harness: "unknown", harnessCommand: null };

/**
 * Harness readiness from a machine's `ao doctor --json` checks.
 *
 * The `claude-auth` check is the whole signal: it asks the harness itself
 * rather than guessing where its credentials live. PASS is signed in; anything
 * else is a WARN, which the check emits for a missing binary, a missing login,
 * and an unreadable answer alike. It never returns FAIL, so nothing here
 * branches on FAIL. The command shown comes from the check's `remediation`
 * field, which exists precisely so this does not have to hardcode one.
 */
export function harnessFromDoctorChecks(raw: unknown): HarnessReadiness {
	const checks = Array.isArray(raw) ? raw : (raw as { checks?: unknown } | null | undefined)?.checks;
	if (!Array.isArray(checks)) return HARNESS_UNKNOWN;
	const check = checks.find(
		(candidate): candidate is DoctorCheck =>
			!!candidate && typeof candidate === "object" && (candidate as DoctorCheck).name === HARNESS_DOCTOR_CHECK,
	);
	if (!check || typeof check.level !== "string") return HARNESS_UNKNOWN;
	if (check.level === "PASS") return { harness: "ready", harnessCommand: null };
	const remediation = typeof check.remediation === "string" ? check.remediation.trim() : "";
	return { harness: "missing", harnessCommand: remediation || HARNESS_SETUP_COMMAND };
}

/**
 * Where a machine's harness readiness comes from. The one seam to change when
 * the data source moves.
 *
 * Today `ao doctor` is a local CLI surface with no HTTP route, so nothing
 * carries a registered machine's checks to this app and this is "unknown" for
 * every one of them. Unknown deliberately claims nothing: it neither tells a
 * user to run a command they may not need, nor reports a harness as ready
 * without having checked. When the daemon route lands, read the checks here;
 * no caller and nothing in the UI changes.
 */
export function readMachineHarness(row: { doctor?: unknown }): HarnessReadiness {
	return harnessFromDoctorChecks(row.doctor);
}

function parseMachine(raw: unknown): AoMachine | null {
	if (!raw || typeof raw !== "object") return null;
	const row = raw as Record<string, unknown>;
	const id = typeof row.id === "string" ? row.id.trim() : "";
	if (!id || id === LOCAL_MACHINE_ID) return null;
	const baseUrl = parseMachineOrigin(row.public_url);
	// A row the app cannot point itself at is not offerable, and offering it
	// would mean a picker entry that silently does nothing when clicked.
	if (!baseUrl) return null;
	const name = typeof row.name === "string" && row.name.trim() ? row.name.trim() : new URL(baseUrl).hostname;
	return {
		id,
		name,
		baseUrl,
		local: false,
		createdAt: optionalIso(row.created_at),
		lastSeen: optionalIso(row.last_seen),
		// Filled in by the liveness probe; the control plane does not know.
		reachability: "unknown",
		...readMachineHarness(row),
	};
}

/**
 * Parse the `GET /api/v1/machines` body. Unusable rows are dropped rather than
 * failing the whole list, so one malformed registration does not hide every
 * other machine the account owns.
 */
export function parseMachinesResponse(body: unknown): AoMachine[] {
	const rows = (body as { machines?: unknown } | null | undefined)?.machines;
	if (!Array.isArray(rows)) return [];
	return rows.map(parseMachine).filter((machine): machine is AoMachine => machine !== null);
}

const SECOND_MS = 1000;
const MINUTE_MS = 60 * SECOND_MS;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

// unit, its length in ms, and how many of it before the next unit reads better.
const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number, number]> = [
	["second", SECOND_MS, 60],
	["minute", MINUTE_MS, 60],
	["hour", HOUR_MS, 24],
];

/** "3 minutes ago", or null when the machine has never been seen. */
export function formatLastSeen(lastSeen: string | null, now = Date.now()): string | null {
	if (!lastSeen) return null;
	const at = Date.parse(lastSeen);
	if (Number.isNaN(at)) return null;
	const elapsed = Math.max(0, now - at);
	const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
	for (const [unit, ms, limit] of RELATIVE_UNITS) {
		if (elapsed < ms * limit) return format.format(-Math.round(elapsed / ms), unit);
	}
	return format.format(-Math.round(elapsed / DAY_MS), "day");
}
