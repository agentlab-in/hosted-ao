// Clone-by-URL support for the project creation flow.
//
// `parseCloneUrl` mirrors the daemon's own parser
// (backend/internal/service/project/clone.go `parseCloneURL`) so a URL the
// daemon would reject as CLONE_URL_INVALID is caught in the field, before a
// round trip that can take minutes. Keep the two in sync: the daemon stays the
// authority, this is only an early exit.

// scp-like git remote syntax, e.g. "git@github.com:owner/repo.git" — a form no
// URL parser recognizes as having a scheme and host.
const SCP_LIKE_CLONE_URL = /^[\w.-]+@[\w.-]+:(.+)$/;

// A parsed owner/repo segment becomes a single path component of the clone
// destination on the daemon, so it is restricted the same way there.
const SAFE_DIR_COMPONENT = /^[A-Za-z0-9._-]+$/;

export type CloneTarget = { owner: string; repo: string };

/** Owner and repository of a git remote URL, or null when it is not one. */
export function parseCloneUrl(raw: string): CloneTarget | null {
	const value = raw.trim();
	if (value === "") return null;

	let pathPart: string | null = null;
	try {
		pathPart = new URL(value).pathname;
	} catch {
		pathPart = SCP_LIKE_CLONE_URL.exec(value)?.[1] ?? null;
	}
	if (pathPart === null) return null;

	const segments = pathPart
		.replace(/^\/+/, "")
		.replace(/\/+$/, "")
		.replace(/\.git$/, "")
		.split("/");
	const repo = segments[segments.length - 1];
	const owner = segments[segments.length - 2];
	if (segments.length < 2 || !SAFE_DIR_COMPONENT.test(owner) || !SAFE_DIR_COMPONENT.test(repo)) return null;
	return { owner, repo };
}

/** "owner/repo" for display, falling back to the raw URL when unparseable. */
export function cloneUrlLabel(raw: string): string {
	const target = parseCloneUrl(raw);
	return target ? `${target.owner}/${target.repo}` : raw.trim();
}

// The daemon returns remediation-shaped clone failures rather than raw git
// output: the message already tells the user what to do (run `gh auth login`,
// accept a host key, pick a different URL). Each code only needs a heading, so
// the user sees what went wrong before reading the fix. Mirrors
// backend/internal/service/project/clone.go.
const CLONE_ERROR_TITLES: Record<string, string> = {
	CLONE_AUTH_FAILED: "Git credentials needed on this machine",
	CLONE_URL_INVALID: "Check the repository URL",
	CLONE_REPO_NOT_FOUND: "Repository not found",
	CLONE_HOST_NOT_FOUND: "Could not reach the git host",
	CLONE_HOST_KEY_UNVERIFIED: "SSH host key not recognized",
	CLONE_TIMEOUT: "Clone timed out",
	CLONE_DESTINATION_EXISTS: "Already cloned on this machine",
	CLONE_DEST_UNAVAILABLE: "Could not prepare the clone destination",
	CLONE_NOT_CONFIGURED: "Cloning by URL is unavailable here",
	CLONE_FAILED: "Could not clone the repository",
	// Structurally unreachable from the UI (a folder and a URL are separate
	// branches of the flow), kept so a future regression reads as a bug rather
	// than as an unexplained failure.
	PATH_AND_CLONE_URL_CONFLICT: "Choose a folder or a URL, not both",
	// Registration failures that can still land on a clone that itself worked.
	PATH_ALREADY_REGISTERED: "Already registered as a project",
	ID_ALREADY_REGISTERED: "Project id already in use",
};

export type CloneErrorPresentation = { title: string; message: string };

/**
 * Heading plus remediation text for a failed clone. `apiErrorMessage` appends
 * "(CODE)" when the daemon message does not already name the code; that suffix
 * is dropped here because the heading now carries the meaning.
 */
export function cloneErrorPresentation(code: string | undefined, message: string): CloneErrorPresentation {
	const trimmed = message.replace(/\s*\(([A-Z0-9_]+)\)\s*$/, "").trim();
	return {
		title: CLONE_ERROR_TITLES[code ?? ""] ?? "Could not clone the repository",
		message: trimmed || "Check the URL and try again.",
	};
}
