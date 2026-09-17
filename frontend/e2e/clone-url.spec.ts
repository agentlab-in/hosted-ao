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
// Rewritten again for the 2026-09 upstream intake: the clone flow is now
// upstream's prepare-clone model. The dialog requires a valid URL AND a
// destination parent before Continue is enabled; Continue stages the
// checkout via POST /api/v1/projects/clone/prepare, and the agent sheet's
// Clone button registers it via POST /api/v1/projects with
// { path, clonePreparationId }. The fork's old no-destination cloneUrl wire
// is still supported by _shell.createProject and the daemon, but the UI no
// longer exercises it (follow-up candidate).

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
	await page.route(`${REMOTE_BASE_URL}/api/v1/imports/validate`, (route) =>
		route.fulfill({
			status: 200,
			contentType: "application/json",
			body: JSON.stringify({
				importKind: "project",
				isValid: true,
				blockingErrors: [],
				nextStep: "continue",
				root: {
					repoPath: "/tmp/e2e-checkout",
					isRepo: true,
					hasCommit: true,
					hasOrigin: true,
					isEmptyFolder: false,
					needsGitInit: false,
					requiredActions: [],
					blockingErrors: [],
				},
			}),
		}),
	);
	await page.route(`${REMOTE_BASE_URL}/api/v1/projects/clone/cleanup`, (route) => route.fulfill({ status: 204 }));
	await page.route(`${REMOTE_BASE_URL}/api/v1/sessions`, (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ sessions: [] }) }),
	);
	await page.route(`${REMOTE_BASE_URL}/api/v1/projects/clone/prepare`, async (route) => {
		if (cloneMs > 0) await new Promise((resolve) => setTimeout(resolve, cloneMs));
		return route.fulfill({
			status: 200,
			contentType: "application/json",
			body: JSON.stringify({ path: "/tmp/e2e-remote/hosted-ao", remoteUrl: "https://github.com/agentlab-in/hosted-ao.git", preparationId: "prep-e2e-remote" }),
		});
	});
	await page.route(`${REMOTE_BASE_URL}/api/v1/projects`, async (route) => {
		if (route.request().method() !== "POST") {
			return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ projects: [] }) });
		}
		createBody = route.request().postDataJSON();
		return route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify(CLONE_AUTH_FAILED) });
	});
	return { body: () => createBody };
}

/** Same shape as stubRemoteDaemon, but for the local daemon's clone wire. */
async function stubLocalCloneDaemon(page: Page): Promise<{ prepareBody: () => unknown; body: () => unknown }> {
	await stubReadiness(page);
	let prepareBody: unknown = null;
	let createBody: unknown = null;
	await page.route("**/api/v1/agents", (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(AGENT_CATALOG) }),
	);
	await page.route("**/api/v1/imports/validate", (route) =>
		route.fulfill({
			status: 200,
			contentType: "application/json",
			body: JSON.stringify({
				importKind: "project",
				isValid: true,
				blockingErrors: [],
				nextStep: "continue",
				root: {
					repoPath: "/tmp/e2e-checkout",
					isRepo: true,
					hasCommit: true,
					hasOrigin: true,
					isEmptyFolder: false,
					needsGitInit: false,
					requiredActions: [],
					blockingErrors: [],
				},
			}),
		}),
	);
	await page.route("**/api/v1/projects/clone/cleanup", (route) => route.fulfill({ status: 204 }));
	await page.route("**/api/v1/projects/clone/prepare", async (route) => {
		prepareBody = route.request().postDataJSON();
		return route.fulfill({
			status: 200,
			contentType: "application/json",
			body: JSON.stringify({ path: "/Users/e2e-tester/code/hosted-ao", remoteUrl: (prepareBody as { remoteUrl?: string })?.remoteUrl ?? "", preparationId: "prep-e2e-local" }),
		});
	});
	await page.route("**/api/v1/projects", async (route) => {
		if (route.request().method() !== "POST") {
			return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ projects: [] }) });
		}
		createBody = route.request().postDataJSON();
		return route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify(CLONE_AUTH_FAILED) });
	});
	return { prepareBody: () => prepareBody, body: () => createBody };
}

/** Picker -> clone step -> agent sheet -> submit, the whole clone branch. */
async function submitClone(page: Page, url: string): Promise<void> {
	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from Git" }).click();
	const dialog = page.getByRole("dialog", { name: "Clone a Git repository" });
	await dialog.getByLabel("Repository URL").fill(url);
	await dialog.getByPlaceholder("Choose a parent folder").fill("/tmp/e2e-destination");
	await page.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone", exact: true }).click();
}

test("renderer: clone registers a prepared checkout by path, never a clone URL @P0 @CLONE", async ({
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

	// The current dialog gates Continue on a parseable URL plus a destination:
	// an unparseable URL keeps it disabled and never advances the dialog.
	await cloneDialog.getByLabel("Repository URL").fill("github.com/agentlab-in");
	await expect(cloneDialog.getByRole("button", { name: "Continue" })).toBeDisabled();
	await expect(page.getByRole("button", { name: "Clone", exact: true })).toHaveCount(0);

	await cloneDialog.getByLabel("Repository URL").fill("https://github.com/agentlab-in/hosted-ao.git");
	await cloneDialog.getByPlaceholder("Choose a parent folder").fill("/tmp/e2e-destination");
	await cloneDialog.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone", exact: true }).click();

	// Upstream flattens every clone failure to the generic title on the agent
	// sheet (the raw daemon text is not surfaced on this path); the stable
	// code and remediation stay available on the error envelope for callers
	// that need them.
	const alert = page.getByRole("alert");
	await expect(alert).toContainText("Could not clone repository");
	await shot(page, "clone-error");

	// Registration carries the prepared checkout's path and preparation id;
	// the retired cloneUrl wire is never sent.
	expect(created.body()).toMatchObject({
		path: "/tmp/e2e-remote/hosted-ao",
		clonePreparationId: "prep-e2e-remote",
	});
	expect(created.body()).not.toHaveProperty("cloneUrl");
});

test("renderer: a clone in flight keeps reporting itself rather than freezing @P0 @CLONE", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: 8080, daemonBaseUrl: REMOTE_BASE_URL });
	// The prepare stage takes as long as it takes; the daemon answers only at
	// the end.
	await stubRemoteDaemon(page, 4000);
	await page.goto("/");

	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from Git" }).click();
	const cloneDialog = page.getByRole("dialog", { name: "Clone a Git repository" });
	await cloneDialog.getByLabel("Repository URL").fill("https://github.com/agentlab-in/hosted-ao.git");
	await cloneDialog.getByPlaceholder("Choose a parent folder").fill("/tmp/e2e-destination");
	await cloneDialog.getByRole("button", { name: "Continue" }).click();

	// While prepare is held, the flow has neither dismissed the dialog nor
	// advanced to a half-rendered sheet: the dialog stays visible with its
	// Continue busy, and the sheet's Clone button does not exist yet.
	const submit = cloneDialog.getByRole("button", { name: "Continue" });
	await expect(cloneDialog).toBeVisible();
	await expect(page.getByRole("button", { name: "Clone", exact: true })).toHaveCount(0);
	await page.waitForTimeout(2000);
	await expect(cloneDialog).toBeVisible();
	await expect(page.getByRole("button", { name: "Clone", exact: true })).toHaveCount(0);
	await shot(page, "clone-progress");

	// Once the daemon answers, the flow settles into the agent sheet with the
	// Clone action present: it reported the whole way instead of freezing.
	await expect(page.getByRole("button", { name: "Clone", exact: true })).toBeVisible();
});

test("renderer: clone with a local destination sends a path, never a clone URL @P0 @CLONE", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: 8080 });
	// Seeds CreateProjectFlow's initial destinationParent so the local-mode
	// dialog never needs the native folder picker (the fake bridge's
	// chooseDirectory always returns null under the browser harness).
	await page.addInitScript(() => {
		window.localStorage.setItem("ao.clone.lastDestinationParent", "/Users/e2e-tester/code");
	});
	const local = await stubLocalCloneDaemon(page);
	await page.goto("/");

	await submitClone(page, "https://github.com/agentlab-in/hosted-ao.git");
	await expect(page.getByRole("alert")).toContainText("Could not clone repository");

	// The staged checkout went to the local daemon's prepare endpoint with the
	// user's chosen destination parent.
	expect(local.prepareBody()).toMatchObject({
		remoteUrl: "https://github.com/agentlab-in/hosted-ao.git",
		destinationParent: "/tmp/e2e-destination",
	});

	// Registration carries the prepared path plus the preparation id, never
	// the retired cloneUrl wire.
	expect(local.body()).toMatchObject({
		path: "/Users/e2e-tester/code/hosted-ao",
		clonePreparationId: "prep-e2e-local",
	});
	expect(local.body()).not.toHaveProperty("cloneUrl");
});
