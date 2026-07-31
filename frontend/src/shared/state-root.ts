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
// still wins outright and is used verbatim.
export const STATE_ROOT_SEGMENTS: readonly string[] = [".ao", "hosted"];
