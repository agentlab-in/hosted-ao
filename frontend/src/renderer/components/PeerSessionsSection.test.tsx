import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PeerWorkspacesResult } from "../../shared/peer-workspaces";

const { navigateMock, peerWorkspacesMock, machinesRefreshMock, switchToMachineMock } = vi.hoisted(() => ({
	navigateMock: vi.fn(),
	peerWorkspacesMock: vi.fn(),
	machinesRefreshMock: vi.fn(),
	switchToMachineMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		machines: {
			refresh: (...args: unknown[]) => machinesRefreshMock(...args),
			peerWorkspaces: (...args: unknown[]) => peerWorkspacesMock(...args),
		},
	},
}));

vi.mock("../lib/peer-session-switch", () => ({
	switchToMachine: (...args: unknown[]) => switchToMachineMock(...args),
}));

import { CloudLocalSections } from "./PeerSessionsSection";

function renderSections(activeBoardContent = <div data-testid="active-board">active board</div>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<CloudLocalSections activeBoardContent={activeBoardContent} />
		</QueryClientProvider>,
	);
}

const okPeer = (overrides: Partial<Extract<PeerWorkspacesResult, { state: "ok" }>> = {}): PeerWorkspacesResult => ({
	state: "ok",
	machineId: "mch_1",
	machineName: "ao-build-01",
	isRemote: true,
	projects: [
		{
			id: "proj_1",
			name: "solkit-ui",
			sessions: [
				{
					id: "peer_s1",
					title: "Fix the header",
					status: "working",
					branch: "ao/dev/fix-header",
					harness: "codex",
					kind: "worker",
					updatedAt: "2026-01-01T00:00:00Z",
				},
			],
		},
	],
	...overrides,
});

beforeEach(() => {
	navigateMock.mockReset();
	peerWorkspacesMock.mockReset().mockResolvedValue(okPeer());
	machinesRefreshMock.mockReset().mockResolvedValue({
		status: "ready",
		machines: [{ id: "local", name: "This Mac", baseUrl: "", local: true, createdAt: null, lastSeen: null, reachability: "online", harness: "ready", harnessCommand: null }],
		activeMachineId: "local",
	});
	switchToMachineMock.mockReset().mockResolvedValue({ status: "ready" });
});

describe("CloudLocalSections", () => {
	it("renders CLOUD first with the peer's sessions, and LOCAL with the active board content", async () => {
		renderSections();

		const cloud = screen.getByTestId("board-section-cloud");
		expect(await within(cloud).findByText("solkit-ui")).toBeInTheDocument();
		expect(within(cloud).getByText("ao-build-01")).toBeInTheDocument();
		expect(within(cloud).getByText("Fix the header")).toBeInTheDocument();

		const local = screen.getByTestId("board-section-local");
		expect(within(local).getByTestId("active-board")).toBeInTheDocument();

		// Cloud renders before Local in document order.
		expect(cloud.compareDocumentPosition(local) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
	});

	it("swaps roles when the peer is local (active machine is the cloud one)", async () => {
		peerWorkspacesMock.mockResolvedValue(okPeer({ isRemote: false, machineName: "This Mac" }));

		renderSections();

		// Resolve the query before capturing either section. While the peer is
		// still loading the component defaults to "peer is cloud", and the swap
		// remounts both sections (one is a component element, the other raw
		// JSX), so a node captured earlier is detached by the time it settles.
		expect(await screen.findByText("solkit-ui")).toBeInTheDocument();

		const local = screen.getByTestId("board-section-local");
		expect(within(local).getByText("solkit-ui")).toBeInTheDocument();
		const cloud = screen.getByTestId("board-section-cloud");
		expect(within(cloud).getByTestId("active-board")).toBeInTheDocument();
	});

	it("shows the peer's reason when unavailable", async () => {
		peerWorkspacesMock.mockResolvedValue({ state: "unavailable", reason: "No cloud machine registered." });

		renderSections();

		expect(await screen.findByText("No cloud machine registered.")).toBeInTheDocument();
	});

	it("shows an empty state when the peer has projects but no sessions", async () => {
		peerWorkspacesMock.mockResolvedValue(okPeer({ projects: [{ id: "p1", name: "empty-proj", sessions: [] }] }));

		renderSections();

		expect(await screen.findByText("No cloud sessions yet.")).toBeInTheDocument();
	});

	it("switches machines and navigates to the session on click", async () => {
		renderSections();

		const row = await screen.findByTestId("peer-session-row");
		await userEvent.click(row);

		expect(switchToMachineMock).toHaveBeenCalledWith("mch_1");
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj_1", sessionId: "peer_s1" },
		});
	});

	it("shows an inline error and does not navigate when the switch fails", async () => {
		switchToMachineMock.mockResolvedValue({ status: "error", message: "Timed out waiting for the machine to become ready." });

		renderSections();

		const row = await screen.findByTestId("peer-session-row");
		await userEvent.click(row);

		expect(await screen.findByTestId("peer-switch-error")).toHaveTextContent(
			"Timed out waiting for the machine to become ready.",
		);
		expect(navigateMock).not.toHaveBeenCalled();
	});
});
