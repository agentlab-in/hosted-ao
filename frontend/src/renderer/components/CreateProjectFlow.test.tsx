import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { agentsQueryKey } from "../hooks/useAgentsQuery";
import { CreateProjectFlow, type CreateProjectInput } from "./CreateProjectFlow";

type CreateProjectHandler = (input: CreateProjectInput) => Promise<void>;

function renderFlow(onCreateProject: CreateProjectHandler) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const agent = { id: "claude-code", label: "claude-code", authStatus: "authorized" };
	queryClient.setQueryData(agentsQueryKey, { supported: [agent], installed: [agent], authorized: [agent] });
	render(
		<QueryClientProvider client={queryClient}>
			<CreateProjectFlow mode="choose" onCreateProject={onCreateProject} onInitializeProject={vi.fn()}>
				{({ choosePath }) => (
					<button type="button" onClick={choosePath}>
						New project
					</button>
				)}
			</CreateProjectFlow>
		</QueryClientProvider>,
	);
}

/** Open the picker, take the clone branch, and land on the URL step. */
async function openCloneStep(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByRole("button", { name: "New project" }));
	await user.click(screen.getByRole("button", { name: "Clone from a Git URL" }));
	return screen.getByRole("dialog", { name: "Clone a repository" });
}

function cloneFailure(code: string, message: string): Error & { code?: string } {
	const failure = new Error(`${message} (${code})`) as Error & { code?: string };
	failure.code = code;
	return failure;
}

describe("CreateProjectFlow clone by URL", () => {
	it("sends a clone URL and never a path alongside it", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		renderFlow(onCreateProject);

		await openCloneStep(user);
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/agentlab-in/hosted-ao.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		await user.click(await screen.findByRole("button", { name: "Clone and start" }));

		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onCreateProject).toHaveBeenCalledWith({
			cloneUrl: "https://github.com/agentlab-in/hosted-ao.git",
			workerAgent: "claude-code",
			orchestratorAgent: "claude-code",
			trackerIntake: undefined,
		});
		expect(vi.mocked(onCreateProject).mock.calls[0][0]).not.toHaveProperty("path");
	});

	it("blocks a URL the daemon would reject, and says what a good one looks like", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		renderFlow(onCreateProject);

		const dialog = await openCloneStep(user);
		expect(within(dialog).getByRole("button", { name: "Continue" })).toBeDisabled();

		await user.type(within(dialog).getByLabelText("Repository URL"), "github.com/owner");
		await user.tab();

		expect(
			within(dialog).getByText("Use an https:// or ssh git remote URL that names an owner and a repository."),
		).toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Continue" })).toBeDisabled();
		expect(onCreateProject).not.toHaveBeenCalled();
	});

	it("shows the daemon's auth remediation on the URL step, with the URL kept for a retry", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi
			.fn()
			.mockRejectedValue(
				cloneFailure(
					"CLONE_AUTH_FAILED",
					"No git credentials on this machine. For an https:// URL, run `gh auth login`. For an SSH URL, add a deploy key or start an SSH agent, then try again.",
				),
			) as CreateProjectHandler;
		renderFlow(onCreateProject);

		await openCloneStep(user);
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/agentlab-in/private-repo.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Clone and start" }));

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Git credentials needed on this machine");
		expect(alert).toHaveTextContent("run gh auth login");
		expect(within(alert).getByText("gh auth login").tagName).toBe("CODE");
		// The code is not repeated raw at the user, and the URL survives for a retry.
		expect(alert).not.toHaveTextContent("CLONE_AUTH_FAILED");
		expect(screen.getByLabelText("Repository URL")).toHaveValue("https://github.com/agentlab-in/private-repo.git");
	});

	it("titles every clone failure code rather than falling back to the raw message", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi
			.fn()
			.mockRejectedValue(
				cloneFailure("CLONE_TIMEOUT", "Clone timed out after 5m0s. Check your network connection and try again."),
			) as CreateProjectHandler;
		renderFlow(onCreateProject);

		await openCloneStep(user);
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/agentlab-in/hosted-ao.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Clone and start" }));

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Clone timed out");
		expect(alert).toHaveTextContent("Check your network connection and try again.");
	});

	it("keeps saying the clone is running while the daemon is still cloning", async () => {
		const user = userEvent.setup();
		let finishClone = () => undefined as void;
		const onCreateProject = vi.fn().mockImplementation(
			() =>
				new Promise<void>((resolve) => {
					finishClone = () => resolve();
				}),
		) as CreateProjectHandler;
		renderFlow(onCreateProject);

		await openCloneStep(user);
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/agentlab-in/hosted-ao.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Clone and start" }));

		const progress = await screen.findByRole("status");
		expect(progress).toHaveTextContent("Cloning agentlab-in/hosted-ao...");
		expect(progress).toHaveTextContent("A large repository can take a few minutes.");
		expect(screen.getByRole("button", { name: "Cloning..." })).toBeDisabled();

		finishClone();
		await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
	});
});
