import { describe, expect, it } from "vitest";
import { cloneErrorPresentation, cloneUrlLabel, parseCloneUrl } from "./clone-url";

describe("parseCloneUrl", () => {
	it.each([
		["https://github.com/agentlab-in/hosted-ao.git", "agentlab-in", "hosted-ao"],
		["https://github.com/agentlab-in/hosted-ao", "agentlab-in", "hosted-ao"],
		["https://github.com/agentlab-in/hosted-ao/", "agentlab-in", "hosted-ao"],
		["git@github.com:agentlab-in/hosted-ao.git", "agentlab-in", "hosted-ao"],
		["ssh://git@github.com/agentlab-in/hosted-ao.git", "agentlab-in", "hosted-ao"],
		["https://gitlab.com/group/sub/repo.git", "sub", "repo"],
		["  https://github.com/owner/repo.git  ", "owner", "repo"],
	])("accepts %s", (raw, owner, repo) => {
		expect(parseCloneUrl(raw)).toEqual({ owner, repo });
	});

	it.each([
		[""],
		["   "],
		["github.com/owner/repo"],
		["https://github.com/repo"],
		["https://github.com/"],
		["not a url at all"],
		["https://github.com/../repo.git"],
		["https://github.com/owner/re po.git"],
	])("rejects %s", (raw) => {
		expect(parseCloneUrl(raw)).toBeNull();
	});
});

describe("cloneUrlLabel", () => {
	it("shortens a parseable URL to owner/repo", () => {
		expect(cloneUrlLabel("https://github.com/agentlab-in/hosted-ao.git")).toBe("agentlab-in/hosted-ao");
	});

	it("falls back to the raw value", () => {
		expect(cloneUrlLabel(" mystery ")).toBe("mystery");
	});
});

describe("cloneErrorPresentation", () => {
	it("titles the auth failure and keeps the daemon's remediation intact", () => {
		const daemonMessage =
			"No git credentials on this machine. For an https:// URL, run `gh auth login`. For an SSH URL, add a deploy key or start an SSH agent, then try again.";

		expect(cloneErrorPresentation("CLONE_AUTH_FAILED", `${daemonMessage} (CLONE_AUTH_FAILED)`)).toEqual({
			title: "Git credentials needed on this machine",
			message: daemonMessage,
		});
	});

	it("falls back to a generic title for an unmapped code", () => {
		expect(cloneErrorPresentation("SOMETHING_ELSE", "Boom")).toEqual({
			title: "Could not clone the repository",
			message: "Boom",
		});
	});

	it("never renders an empty body", () => {
		expect(cloneErrorPresentation("CLONE_FAILED", "(CLONE_FAILED)").message).toBe("Check the URL and try again.");
	});
});
