import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

// The whole point of this file: which wire a clone leaves on depends on whether
// the active machine is this computer. Drive that through the same base URL
// api-client rebases every REST call onto.
const { getApiBaseUrlMock } = vi.hoisted(() => ({ getApiBaseUrlMock: vi.fn(() => "http://127.0.0.1:3001") }));
vi.mock("../lib/api-client", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/api-client")>()),
	getApiBaseUrl: getApiBaseUrlMock,
	subscribeApiBaseUrl: () => () => undefined,
}));

import { agentsQueryKey } from "../hooks/useAgentsQuery";
import { CreateProjectFlow, type CreateProjectInput, type CloneProjectInput } from "./CreateProjectFlow";

function renderFlow() {
	const onCreateProject = vi.fn<(input: CreateProjectInput) => Promise<void>>().mockResolvedValue(undefined);
	const onCloneProject = vi.fn<(input: CloneProjectInput) => Promise<void>>().mockResolvedValue(undefined);
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const agent = { id: "claude-code", label: "claude-code", authStatus: "authorized" };
	queryClient.setQueryData(agentsQueryKey, { supported: [agent], installed: [agent], authorized: [agent] });
	render(
		<QueryClientProvider client={queryClient}>
			<CreateProjectFlow
				mode="choose"
				onCloneProject={onCloneProject}
				onCreateProject={onCreateProject}
				onInitializeProject={vi.fn()}
			>
				{({ choosePath }) => (
					<button type="button" onClick={choosePath}>
						New project
					</button>
				)}
			</CreateProjectFlow>
		</QueryClientProvider>,
	);
	return { onCloneProject, onCreateProject };
}

/** Open the picker, take the clone branch, and land on the URL step. */
async function openCloneStep(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByRole("button", { name: "New project" }));
	await user.click(await screen.findByRole("button", { name: /Clone from Git/ }));
	await screen.findByLabelText("Repository URL");
}

describe("CreateProjectFlow clone routing", () => {
	it("sends cloneUrl, and never a local destination, when the active machine is remote", async () => {
		getApiBaseUrlMock.mockReturnValue("https://vm.example.com");
		const user = userEvent.setup();
		const { onCloneProject, onCreateProject } = renderFlow();

		await openCloneStep(user);
		// No local folder picker is even offered on a remote machine.
		expect(screen.queryByRole("button", { name: "Choose" })).not.toBeInTheDocument();
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/agentlab-in/hosted-ao.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Clone and start" }));

		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		// POST /api/v1/projects with cloneUrl: the daemon on the machine picks the
		// destination. POST /api/v1/projects/clone is never used, because its
		// destinationParent would be a path from this desktop's filesystem.
		expect(onCloneProject).not.toHaveBeenCalled();
		const sent = onCreateProject.mock.calls[0][0];
		expect(sent).toMatchObject({ cloneUrl: "https://github.com/agentlab-in/hosted-ao.git" });
		expect(sent).not.toHaveProperty("path");
		expect(sent).not.toHaveProperty("destinationParent");
	});

	it("keeps upstream's destination-picker flow when the active machine is local", async () => {
		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:3001");
		const user = userEvent.setup();
		renderFlow();

		await openCloneStep(user);

		expect(screen.getByLabelText("Clone into")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Choose" })).toBeInTheDocument();
	});
});
