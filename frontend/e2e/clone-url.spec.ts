import { expect, test, type Page } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeBridge } from "./support/fake-bridge";

// CLONE-* RENDERER SMOKE (renderer slice, issue #59).
//
// Scope: `dev:web` + fake bridge, with the daemon's REST answers stubbed at
// the network. Unlike the vitest coverage of the same flow, this drives the
// real generated API client, so it locks the wire shape each clone path
// sends (`cloneUrl` on the remote wire, `remoteUrl`/`destinationParent` on
// the local wire, never mixed) and the remediation the daemon's error
// envelope turns into on screen. It does NOT exercise a real clone: that
// boundary is the daemon's, covered by backend tests.
//
// Rewritten for the upstream merge (#98): the fork's own "Clone from a Git
// URL" dialog, its disabled-Continue-on-invalid-URL behavior, and its
// "Cloning {{url}}..." elapsed-counter progress UI were all replaced or
// deleted outright. See CloneRepositoryDialog.tsx and
// CreateProjectAgentSheet.tsx for the current flow;
// src/renderer/lib/clone-url.ts's cloneErrorPresentation/cloneUrlLabel are
// now dead code with no callers, so nothing here exercises them.

const AGENT_CATALOG = {
	supported: [{ id: "claude-code", label: "Claude Code" }],
	installed: [{ id: "claude-code", label: "Claude Code", authStatus: "authorized" }],
	authorized: [{ id: "claude-code", label: "Claude Code", authStatus: "authorized" }],
};

const CLONE_AUTH_FAILED = {
	error: "invalid_request",
	code: "CLONE_AUTH_FAILED",
	message:
		"No git credentials on this machine. For an https:// URL, run `gh auth login`. For an SSH URL, add a deploy key or start an SSH agent, then try again.",
	requestId: "req_clone_demo",
};

// The active machine's daemon base URL once the fake bridge is put in remote
// mode (see installFakeBridge's `daemonBaseUrl` option). Any HTTPS origin
// works: Playwright intercepts the request before it needs to resolve.
const REMOTE_BASE_URL = "https://fake-daemon.e2e.test";

// Demo capture for `ao preview`, off in CI: waits out the modal's open
// animation so the shot is not a half-faded frame.
async function shot(page: Page, name: string): Promise<void> {
	if (!process.env.AO_CLONE_SHOTS) return;
	await page.waitForTimeout(400);
	await page.screenshot({ path: `${process.env.AO_CLONE_SHOTS}/${name}.png` });
}

async function stubReadiness(page: Page): Promise<void> {
	await page.route(/\/api\/v1\/agents\/readiness(?:\/ensure)?$/, (route) =>
		route.fulfill({
			status: 200,
			contentType: "application/json",
			body: JSON.stringify({ agents: [agentReadiness("claude-code", "Claude Code")] }),
		}),
	);
}

/**
 * Answers the calls the remote (cloneUrl) clone step makes against
 * REMOTE_BASE_URL, captures the POST /api/v1/projects body, and can hold
 * that call open the way a real clone does. GET /api/v1/projects and
 * /api/v1/sessions (the board's workspace query, which also rebases onto
 * this origin in remote mode) are answered empty so the shell settles
 * instead of racing an unstubbed DNS failure.
 */
async function stubRemoteDaemon(page: Page, cloneMs = 0): Promise<{ body: () => unknown }> {
	await stubReadiness(page);
	let createBody: unknown = null;
	await page.route(`${REMOTE_BASE_URL}/api/v1/agents`, (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(AGENT_CATALOG) }),
	);
	await page.route(`${REMOTE_BASE_URL}/api/v1/sessions`, (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ sessions: [] }) }),
	);
	await page.route(`${REMOTE_BASE_URL}/api/v1/projects`, async (route) => {
		if (route.request().method() !== "POST") {
			return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ projects: [] }) });
		}
		createBody = route.request().postDataJSON();
		if (cloneMs > 0) await new Promise((resolve) => setTimeout(resolve, cloneMs));
		return route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify(CLONE_AUTH_FAILED) });
	});
	return { body: () => createBody };
}

/** Same shape as stubRemoteDaemon, but for the local daemon's clone wire. */
async function stubLocalCloneDaemon(page: Page): Promise<{ body: () => unknown }> {
	await stubReadiness(page);
	let cloneBody: unknown = null;
	await page.route("**/api/v1/agents", (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(AGENT_CATALOG) }),
	);
	await page.route("**/api/v1/projects/clone", async (route) => {
		cloneBody = route.request().postDataJSON();
		return route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify(CLONE_AUTH_FAILED) });
	});
	return { body: () => cloneBody };
}

/** Picker -> clone step -> agent sheet -> submit, the whole clone branch. */
async function submitClone(page: Page, url: string): Promise<void> {
	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from Git" }).click();
	await page.getByRole("dialog", { name: "Clone a Git repository" }).getByLabel("Repository URL").fill(url);
	await page.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone", exact: true }).click();
}

test("renderer: clone by URL sends only a clone URL and surfaces the daemon's remediation @P0 @CLONE", async ({
	page,
}) => {
	await installFakeBridge(page, { daemonPort: 8080, daemonBaseUrl: REMOTE_BASE_URL });
	const created = await stubRemoteDaemon(page);
	await page.goto("/");

	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from Git" }).click();

	const cloneDialog = page.getByRole("dialog", { name: "Clone a Git repository" });
	await expect(cloneDialog).toBeVisible();
	await shot(page, "clone-field");

	// A URL the daemon would reject never leaves the field. Unlike the fork,
	// Continue is not disabled on an invalid URL: submitting surfaces an
	// inline error instead and never advances past this dialog.
	await cloneDialog.getByLabel("Repository URL").fill("github.com/agentlab-in");
	await cloneDialog.getByRole("button", { name: "Continue" }).click();
	await expect(cloneDialog.getByRole("alert")).toContainText("Enter a valid HTTPS, SSH, Git, or file URL.");
	await expect(cloneDialog.getByLabel("Repository URL")).toHaveValue("github.com/agentlab-in");
	await expect(page.getByRole("button", { name: "Clone", exact: true })).toHaveCount(0);

	await cloneDialog.getByLabel("Repository URL").fill("https://github.com/agentlab-in/hosted-ao.git");
	await cloneDialog.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone", exact: true }).click();

	// The daemon's CLONE_AUTH_FAILED envelope surfaces on the agent sheet
	// (projectSheetError falls through to the generic clone-failed title,
	// since the code has no dedicated case), with the remediation text intact.
	const alert = page.getByRole("alert");
	await expect(alert).toContainText("Could not clone repository");
	await expect(alert).toContainText("gh auth login");
	await shot(page, "clone-error");

	expect(created.body()).toMatchObject({ cloneUrl: "https://github.com/agentlab-in/hosted-ao.git" });
	expect(created.body()).not.toHaveProperty("path");
});

test("renderer: a clone in flight keeps reporting itself rather than freezing @P0 @CLONE", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: 8080, daemonBaseUrl: REMOTE_BASE_URL });
	// A clone takes as long as it takes; the daemon answers only at the end.
	await stubRemoteDaemon(page, 4000);
	await page.goto("/");

	await submitClone(page, "https://github.com/agentlab-in/hosted-ao.git");

	// The fork's "Cloning {{url}}..." status region with a moving elapsed
	// counter was dead code the upstream merge deleted outright: no callers,
	// no elapsed counter anywhere in the new flow. The busy submit label is
	// now the liveness signal, so a frozen dialog is what this has to catch.
	const sheet = page.getByRole("dialog", { name: "Project agents" });
	const submit = page.getByRole("button", { name: "Cloning..." });
	await expect(submit).toBeVisible();
	await expect(submit).toBeDisabled();
	await shot(page, "clone-progress");

	// Still busy partway through the 4s hold: the sheet has not dismissed,
	// blanked, or otherwise stopped reporting itself.
	await page.waitForTimeout(2000);
	await expect(sheet).toBeVisible();
	await expect(submit).toBeVisible();
	await expect(submit).toBeDisabled();
});

test("renderer: clone with a local destination sends a path, never a clone URL @P0 @CLONE", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: 8080 });
	// Seeds CreateProjectFlow's initial destinationParent so the local-mode
	// dialog never needs the native folder picker (the fake bridge's
	// chooseDirectory always returns null under the browser harness).
	await page.addInitScript(() => {
		window.localStorage.setItem("ao.clone.lastDestinationParent", "/Users/e2e-tester/code");
	});
	const cloned = await stubLocalCloneDaemon(page);
	await page.goto("/");

	await submitClone(page, "https://github.com/agentlab-in/hosted-ao.git");
	await expect(page.getByRole("alert")).toContainText("gh auth login");

	// Mirror image of the remote-wire assertion above: a local destination
	// routes onto POST /api/v1/projects/clone with remoteUrl+destinationParent,
	// never the cloneUrl the remote wire uses.
	expect(cloned.body()).toMatchObject({
		remoteUrl: "https://github.com/agentlab-in/hosted-ao.git",
		destinationParent: "/Users/e2e-tester/code",
	});
	expect(cloned.body()).not.toHaveProperty("cloneUrl");
});
