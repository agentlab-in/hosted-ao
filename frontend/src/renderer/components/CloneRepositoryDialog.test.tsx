import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import CloneRepositoryDialog, { type CloneRepositoryDetails } from "./CloneRepositoryDialog";

// The destination picker is this desktop's own folder dialog, so on a remote
// machine it would hand a local macOS path to a Linux daemon. These tests pin
// the two shapes apart: the local flow stays exactly as upstream shipped it,
// and the remote flow drops the picker and submits a URL only, which is what
// routes the create onto the fork's `cloneUrl` wire (POST /api/v1/projects).

function renderDialog(
	overrides: {
		remote?: boolean;
		value?: CloneRepositoryDetails;
		onContinue?: (selection: { remoteUrl: string; destinationParent: string; targetPath: string }) => void;
	} = {},
) {
	const onContinue = overrides.onContinue ?? vi.fn();
	render(
		<CloneRepositoryDialog
			disabled={false}
			error={null}
			onBack={vi.fn()}
			onChange={vi.fn()}
			onClose={vi.fn()}
			onContinue={onContinue}
			open
			remote={overrides.remote ?? false}
			value={overrides.value ?? { remoteUrl: "", destinationParent: "" }}
		/>,
	);
	return { onContinue };
}

describe("CloneRepositoryDialog on a remote machine", () => {
	it("does not offer the local destination picker", () => {
		renderDialog({ remote: true });

		expect(screen.queryByLabelText("Clone into")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Choose" })).not.toBeInTheDocument();
		// The URL field is still the whole form.
		expect(screen.getByLabelText("Repository URL")).toBeInTheDocument();
	});

	it("describes the remote clone flow, not the local destination picker", () => {
		renderDialog({ remote: true });

		expect(
			screen.getByText("AO clones the repository onto the active machine, then imports the clone as a project."),
		).toBeInTheDocument();
	});

	it("submits a URL with no destination, so the flow takes the cloneUrl wire", async () => {
		const user = userEvent.setup();
		const onContinue = vi.fn();
		renderDialog({
			remote: true,
			onContinue,
			value: { remoteUrl: "https://github.com/agentlab-in/hosted-ao.git", destinationParent: "" },
		});

		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(onContinue).toHaveBeenCalledTimes(1);
		expect(onContinue).toHaveBeenCalledWith({
			remoteUrl: "https://github.com/agentlab-in/hosted-ao.git",
			// The empty string is load-bearing: CreateProjectFlow discriminates on it.
			destinationParent: "",
			// The daemon names the directory <owner>-<repo>; the preview must match.
			targetPath: "agentlab-in-hosted-ao",
		});
	});

	it("previews the directory name the daemon will actually create", () => {
		renderDialog({
			remote: true,
			value: { remoteUrl: "git@github.com:agentlab-in/hosted-ao.git", destinationParent: "" },
		});

		expect(screen.getByText("agentlab-in-hosted-ao")).toBeInTheDocument();
	});

	it("rejects a URL the remote daemon's parser would reject", async () => {
		const user = userEvent.setup();
		const onContinue = vi.fn();
		// Single-segment path: upstream's repositoryNameFromGitUrl accepts it (it
		// only needs a last segment), but the cloneUrl wire needs owner AND repo to
		// build <owner>-<repo>, so it must fail in the field, not after a round trip.
		renderDialog({ remote: true, onContinue, value: { remoteUrl: "https://example.com/repo.git", destinationParent: "" } });

		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(onContinue).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent("Enter a valid HTTPS, SSH, Git, or file URL.");
	});
});

describe("CloneRepositoryDialog on the local machine", () => {
	it("keeps upstream's destination picker", () => {
		renderDialog({ remote: false });

		expect(screen.getByLabelText("Clone into")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Choose" })).toBeInTheDocument();
	});

	it("describes choosing a local destination, not the remote-machine flow", () => {
		renderDialog({ remote: false });

		expect(
			screen.getByText("Paste a Git URL and choose where AO should create the local checkout."),
		).toBeInTheDocument();
	});

	it("still requires a destination before continuing", async () => {
		const user = userEvent.setup();
		const onContinue = vi.fn();
		renderDialog({
			remote: false,
			onContinue,
			value: { remoteUrl: "https://github.com/agentlab-in/hosted-ao.git", destinationParent: "" },
		});

		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(onContinue).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent("Choose a destination folder.");
	});

	it("submits the joined local path when a destination is chosen", async () => {
		const user = userEvent.setup();
		const onContinue = vi.fn();
		renderDialog({
			remote: false,
			onContinue,
			value: { remoteUrl: "https://github.com/agentlab-in/hosted-ao.git", destinationParent: "/Users/dev/code" },
		});

		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(onContinue).toHaveBeenCalledWith({
			remoteUrl: "https://github.com/agentlab-in/hosted-ao.git",
			destinationParent: "/Users/dev/code",
			targetPath: "/Users/dev/code/hosted-ao",
		});
	});
});
