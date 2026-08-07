import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import type { AoAccountState } from "../../../shared/ao-account";
import { AccountSection } from "./AccountSection";

const { getState, signIn, signOut } = vi.hoisted(() => ({
	getState: vi.fn(),
	signIn: vi.fn(),
	signOut: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: { account: { getState, signIn, signOut } },
}));

const SIGNED_OUT: AoAccountState = { status: "signed-out", controlPlaneUrl: "https://ao.agentlab.in" };
const SIGNED_IN: AoAccountState = {
	status: "signed-in",
	controlPlaneUrl: "https://ao.agentlab.in",
	account: { id: "acct_1", email: "dev@example.com" },
};

function renderSection() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<AccountSection />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	getState.mockReset().mockResolvedValue(SIGNED_OUT);
	signIn.mockReset().mockResolvedValue(SIGNED_IN);
	signOut.mockReset().mockResolvedValue(SIGNED_OUT);
});

test("signed out offers sign-in and says an account is not needed locally", async () => {
	renderSection();
	expect(await screen.findByText("Not signed in")).toBeInTheDocument();
	expect(screen.getByText(/works without an account/i)).toBeInTheDocument();

	await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
	expect(signIn).toHaveBeenCalledTimes(1);
	expect(await screen.findByText("dev@example.com")).toBeInTheDocument();
	expect(screen.getByRole("button", { name: /sign out/i })).toBeInTheDocument();
});

test("signed in shows the account and signs out through the bridge", async () => {
	getState.mockResolvedValue(SIGNED_IN);
	renderSection();
	await userEvent.click(await screen.findByRole("button", { name: /sign out/i }));
	expect(signOut).toHaveBeenCalledTimes(1);
	expect(await screen.findByText("Not signed in")).toBeInTheDocument();
});

test("a failed sign-in shows the reason and keeps the sign-in button", async () => {
	signIn.mockResolvedValue({
		...SIGNED_OUT,
		error: "Could not reach the AO control plane at https://ao.agentlab.in.",
	} satisfies AoAccountState);
	renderSection();
	await userEvent.click(await screen.findByRole("button", { name: /sign in/i }));
	expect(await screen.findByTestId("ao-account-error")).toHaveTextContent(/Could not reach the AO control plane/);
	expect(screen.getByRole("button", { name: /sign in/i })).toBeEnabled();
});

test("no credential store disables sign-in and explains why", async () => {
	getState.mockResolvedValue({
		status: "unavailable",
		controlPlaneUrl: "https://ao.agentlab.in",
		error: "This system has no OS credential store available.",
	} satisfies AoAccountState);
	renderSection();
	expect(await screen.findByTestId("ao-account-error")).toHaveTextContent(/no OS credential store/);
	expect(screen.getByRole("button", { name: /sign in/i })).toBeDisabled();
	expect(signIn).not.toHaveBeenCalled();
});

test("a development control plane is named in the UI, never silent", async () => {
	getState.mockResolvedValue({ status: "signed-out", controlPlaneUrl: "http://127.0.0.1:8080" } satisfies AoAccountState);
	renderSection();
	expect(await screen.findByText(/Development control plane/)).toHaveTextContent("http://127.0.0.1:8080");
});
