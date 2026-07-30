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

/**
 * Harness readiness for one machine, from an optional `harness` object on the
 * row: `{"ready": false, "command": "ao vm setup-harness claude"}`.
 *
 * Readiness itself is produced by the machine's own `ao doctor` checks, which
 * task 11 (issue #30) is adding in parallel; how the control plane will carry
 * that summary on this route is not settled yet. So the field is read when it
 * is there and the state is "unknown" when it is not. Unknown deliberately
 * claims nothing: it neither tells a user to run a command they may not need,
 * nor reports a harness as ready without having checked.
 */
function parseHarness(raw: unknown): Pick<AoMachine, "harness" | "harnessCommand"> {
	if (!raw || typeof raw !== "object") return { harness: "unknown", harnessCommand: null };
	const { ready, command } = raw as { ready?: unknown; command?: unknown };
	if (typeof ready !== "boolean") return { harness: "unknown", harnessCommand: null };
	if (ready) return { harness: "ready", harnessCommand: null };
	return {
		harness: "missing",
		harnessCommand: typeof command === "string" && command.trim() ? command.trim() : HARNESS_SETUP_COMMAND,
	};
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
		...parseHarness(row.harness),
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
