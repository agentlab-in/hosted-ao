import path from "node:path";

// The upstream agent-orchestrator desktop app writes its running.json, its
// ao.db and its worktrees directly under ~/.ao. If hosted-ao defaulted to the
// same directory, the two apps would fight over daemon discovery (running.json,
// the pid, the port) and over ao.db. Defaulting one level down keeps hosted-ao
// out of its way. Mirrors backend/internal/config's StateRootSegments; the two
// sides must agree exactly or daemon discovery breaks.
//
// The hard rule is unchanged: state stays under ~/.ao and never lands in an
// OS-default app-data location (e.g. ~/Library/Application Support).
//
// AO_DATA_DIR and AO_RUN_FILE are untouched by this: an explicit override
// still wins and is normalized against the process launch cwd.
export const STATE_ROOT_SEGMENTS: readonly string[] = [".ao", "hosted"];

/** Match Go's runtime-root precedence before any child changes its working directory.
 * Explicit run/data paths remain independent. Gateway identity uses its own default. */
export function resolveRuntimePaths(
	env: Record<string, string | undefined>,
	homeDir: string,
	launchWorkingDirectory: string,
	platform: NodeJS.Platform = process.platform,
	development = false,
): { stateRoot: string; runFile: string; dataDir: string; userData: string } {
	if ([env.AO_RUN_FILE, env.AO_DATA_DIR, homeDir, launchWorkingDirectory].some((value) => value?.includes("\0"))) {
		throw new Error("AO state path contains NUL");
	}
	const paths = platform === "win32" ? path.win32 : path.posix;
	const explicitRunFile = env.AO_RUN_FILE?.trim();
	const explicitDataDir = env.AO_DATA_DIR?.trim();
	const runFile = explicitRunFile ? paths.resolve(launchWorkingDirectory, explicitRunFile) : undefined;
	const dataDir = explicitDataDir ? paths.resolve(launchWorkingDirectory, explicitDataDir) : undefined;
	if (!runFile && !dataDir && !homeDir) throw new Error("AO home directory is unavailable");
	const stateRoot = runFile ? paths.dirname(runFile)
		: dataDir ? paths.dirname(dataDir)
		: paths.resolve(homeDir, ...STATE_ROOT_SEGMENTS, ...(development ? ["dev"] : []));
	return {
		stateRoot,
		runFile: runFile ?? paths.join(stateRoot, "running.json"),
		dataDir: dataDir ?? paths.join(stateRoot, "data"),
		userData: paths.join(stateRoot, "electron"),
	};
}
