import { expect, test, type Page } from "@playwright/test";
import { installFakeBridge } from "./support/fake-bridge";

// CLONE-* RENDERER SMOKE (renderer slice, issue #59).
//
// Scope: `dev:web` + fake bridge, with the daemon's two REST answers stubbed at
// the network. Unlike the vitest coverage of the same flow, this drives the
// real generated API client, so it locks the wire shape the clone step sends
// (`cloneUrl`, never `path`) and the remediation the daemon's error envelope
// turns into on screen. It does NOT exercise a real clone: that boundary is the
// daemon's, covered by backend tests.

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

// Demo capture for `ao preview`, off in CI: waits out the modal's open
// animation so the shot is not a half-faded frame.
async function shot(page: Page, name: string): Promise<void> {
	if (!process.env.AO_CLONE_SHOTS) return;
	await page.waitForTimeout(400);
	await page.screenshot({ path: `${process.env.AO_CLONE_SHOTS}/${name}.png` });
}

/**
 * Answers the two daemon calls the clone step makes, captures the create body,
 * and can hold the create call open the way a real clone does.
 */
async function stubDaemon(page: Page, cloneMs = 0): Promise<{ body: () => unknown }> {
	let createBody: unknown = null;
	await page.route("**/api/v1/agents", (route) =>
		route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(AGENT_CATALOG) }),
	);
	await page.route("**/api/v1/projects", async (route) => {
		if (route.request().method() !== "POST") return route.fallback();
		createBody = route.request().postDataJSON();
		if (cloneMs > 0) await new Promise((resolve) => setTimeout(resolve, cloneMs));
		return route.fulfill({
			status: 400,
			contentType: "application/json",
			body: JSON.stringify(CLONE_AUTH_FAILED),
		});
	});
	return { body: () => createBody };
}

/** Picker → clone step → agent sheet → submit, the whole clone branch. */
async function submitClone(page: Page, url: string): Promise<void> {
	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from a Git URL" }).click();
	await page.getByRole("dialog", { name: "Clone a repository" }).getByLabel("Repository URL").fill(url);
	await page.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone and start" }).click();
}

test("renderer: clone by URL sends only a clone URL and surfaces the daemon's remediation @P0 @CLONE", async ({
	page,
}) => {
	await installFakeBridge(page, { daemonPort: 8080 });
	const created = await stubDaemon(page);
	await page.goto("/");

	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "Clone from a Git URL" }).click();

	const cloneDialog = page.getByRole("dialog", { name: "Clone a repository" });
	await expect(cloneDialog).toBeVisible();
	await shot(page, "clone-field");

	// A URL the daemon would reject never leaves the field.
	await cloneDialog.getByLabel("Repository URL").fill("github.com/agentlab-in");
	await expect(cloneDialog.getByRole("button", { name: "Continue" })).toBeDisabled();

	await cloneDialog.getByLabel("Repository URL").fill("https://github.com/agentlab-in/hosted-ao.git");
	await cloneDialog.getByRole("button", { name: "Continue" }).click();
	await page.getByRole("button", { name: "Clone and start" }).click();

	const alert = page.getByRole("alert");
	await expect(alert).toContainText("Git credentials needed on this machine");
	await expect(alert.getByText("gh auth login")).toBeVisible();
	await expect(cloneDialog.getByLabel("Repository URL")).toHaveValue("https://github.com/agentlab-in/hosted-ao.git");
	await shot(page, "clone-error");

	expect(created.body()).toMatchObject({ cloneUrl: "https://github.com/agentlab-in/hosted-ao.git" });
	expect(created.body()).not.toHaveProperty("path");
});

test("renderer: a clone in flight keeps reporting itself rather than freezing @P0 @CLONE", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: 8080 });
	// A clone takes as long as it takes; the daemon answers only at the end.
	await stubDaemon(page, 4000);
	await page.goto("/");

	await submitClone(page, "https://github.com/agentlab-in/hosted-ao.git");

	const progress = page.getByRole("status").filter({ hasText: "Cloning agentlab-in/hosted-ao" });
	await expect(progress).toBeVisible();
	await expect(page.getByRole("button", { name: "Cloning..." })).toBeDisabled();
	await shot(page, "clone-progress");
	// The elapsed counter is the liveness signal, so it has to actually move.
	await expect(progress).toContainText("0:02", { timeout: 4000 });
});
