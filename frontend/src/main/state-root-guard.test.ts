import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// Hosted AO keeps every byte of its state under ~/.ao/hosted. The upstream
// agent-orchestrator app writes its running.json, ao.db and worktrees directly
// under ~/.ao, so a bare ~/.ao here makes the two builds fight over daemon
// discovery, the pid, the port, a SQLite file whose goose history the other
// build owns, and (as an upstream merge actually delivered) cloud-auth.bin and
// the agent-browser socket run root.
//
// This guard exists because that regression class arrives through clean merges,
// not conflicts: upstream adds a new ~/.ao consumer, git merges it without a
// murmur, and grepping for the CORRECT pattern (STATE_ROOT_SEGMENTS) cannot
// fail. The only check that catches it is the inverse one, so it lives here as
// a test rather than as a habit.
// The jsdom environment rewrites import.meta.url to an http URL, so anchor on
// vitest's own root (frontend/) instead. The second test below fails loudly if
// this ever resolves somewhere with no sources in it.
const SRC_ROOT = path.resolve(process.cwd(), "src");

// Patterns that mean "derive a path from the home directory with a bare .ao
// segment". The first catches the direct form, the second catches the form
// where homedir() was stashed in a local first.
const BARE_STATE_ROOT = [
	/homedir\(\)\s*,\s*(["'`])\.ao\1/,
	/\bhome(?:Dir)?\s*,\s*(["'`])\.ao\1/,
];

// state-root.ts is where the segments are spelled once, on purpose.
// import-folder-scan.ts refuses a project folder anywhere inside ~/.ao, which is
// deliberately the whole tree (a superset of ~/.ao/hosted) and writes nothing.
const ALLOWED = new Set([
	path.join(SRC_ROOT, "shared", "state-root.ts"),
	path.join(SRC_ROOT, "main", "import-folder-scan.ts"),
	path.join(SRC_ROOT, "main", "import-folder-scan.test.ts"),
	path.join(SRC_ROOT, "main", "state-root-guard.test.ts"),
]);

function sourceFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) return entry.name === "node_modules" ? [] : sourceFiles(full);
		return /\.tsx?$/.test(entry.name) ? [full] : [];
	});
}

describe("state root", () => {
	it("never joins the home directory to a bare .ao outside state-root.ts", () => {
		const offenders: string[] = [];
		for (const file of sourceFiles(SRC_ROOT)) {
			if (ALLOWED.has(file)) continue;
			const lines = readFileSync(file, "utf8").split("\n");
			lines.forEach((line, index) => {
				if (BARE_STATE_ROOT.some((pattern) => pattern.test(line))) {
					offenders.push(`${path.relative(SRC_ROOT, file)}:${index + 1}: ${line.trim()}`);
				}
			});
		}
		expect(
			offenders,
			`These sites resolve app state under a bare ~/.ao, which the upstream ` +
				`agent-orchestrator build already owns. Derive the path from ` +
				`STATE_ROOT_SEGMENTS (shared/state-root.ts) instead of respelling it:\n` +
				offenders.join("\n"),
		).toEqual([]);
	});

	it("finds the sources it is supposed to be scanning", () => {
		// A broken SRC_ROOT would make the assertion above pass vacuously.
		const files = sourceFiles(SRC_ROOT);
		expect(files.length).toBeGreaterThan(100);
		expect(files).toContain(path.join(SRC_ROOT, "main.ts"));
	});
});
