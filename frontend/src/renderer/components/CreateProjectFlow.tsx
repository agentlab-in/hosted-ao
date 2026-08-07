import * as Dialog from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { CheckCircle2, ChevronRight, CloudDownload, Folder, FolderPlus, TriangleAlert, X, XCircle } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { ImportFolderScan } from "../../preload";
import { aoBridge } from "../lib/bridge";
import { cloneErrorPresentation, parseCloneUrl } from "../lib/clone-url";
import { cn } from "../lib/utils";
import type { ProjectKind } from "../types/workspace";
import { CreateProjectAgentSheet, type CreateProjectAgentSelection } from "./CreateProjectAgentSheet";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

// A local folder and a clone URL are mutually exclusive on the daemon (it
// answers PATH_AND_CLONE_URL_CONFLICT when both arrive). Modelling the source
// as a union means the UI cannot express the conflict at all: the two are
// separate branches of the flow, and nothing can send both.
export type CreateProjectSource = { path: string; cloneUrl?: never } | { cloneUrl: string; path?: never };

export type CreateProjectInput = CreateProjectSource & { asWorkspace?: boolean } & CreateProjectAgentSelection;

type CreateProjectFlowMode = ProjectKind | "choose";

// Shared create-project flow (native folder picker -> agent sheet -> create).
// Sidebar opens the import-type picker as a dialog; the first-run board embeds
// the same picker inline. Both still share the Git setup recovery path.
export function CreateProjectFlow({
	children,
	embedded = false,
	idleLabel,
	mode = "single_repo",
	onCreateProject,
	onInitializeProject,
	openSignal,
}: {
	children?: (state: { choosePath: () => void; disabled: boolean; error: string | null; label: string }) => ReactNode;
	// When true, render the Workspace/Project chooser inline (start page) instead
	// of behind a trigger + dialog. Folder validation + agent sheet stay modal.
	embedded?: boolean;
	idleLabel?: string;
	mode?: CreateProjectFlowMode;
	onCreateProject: (input: CreateProjectInput) => Promise<void>;
	onInitializeProject: (path: string) => Promise<void>;
	// Monotonic counter: each new value opens the flow programmatically (the ⌘N
	// "no project in scope" fallback). Lets the shortcut reuse the sidebar's own
	// create-project flow instead of a separate delegating component.
	openSignal?: number;
}) {
	const { t } = useTranslation();
	const resolvedIdleLabel = idleLabel ?? t("createProject.newProject");
	const [error, setError] = useState<string | null>(null);
	const [errorCode, setErrorCode] = useState<string | null>(null);
	const [modePickerOpen, setModePickerOpen] = useState(false);
	const [folderPickerOpen, setFolderPickerOpen] = useState(false);
	const [cloneDialogOpen, setCloneDialogOpen] = useState(false);
	// The field's text, kept across a failed clone so a retry (after `gh auth
	// login`, say) does not start from an empty input.
	const [cloneUrl, setCloneUrl] = useState("");
	// Set once the URL is confirmed: the agent sheet is open and this is what
	// gets cloned on submit.
	const [pendingCloneUrl, setPendingCloneUrl] = useState<string | null>(null);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [repositorySetup, setRepositorySetup] = useState<"NOT_A_GIT_REPO" | "PROJECT_UNBORN" | null>(null);
	const [repositorySetupWarning, setRepositorySetupWarning] = useState<string | null>(null);

	const hasModePicker = mode === "choose";
	const isBusy = isChoosingPath || isCreating || isInitializing;

	const openFolderStep = (kind: ProjectKind) => {
		// Keep the selector mounted behind the native picker. Closing it first
		// exposes a blank compositor frame on Windows before Explorer takes focus.
		void chooseDirectory(kind);
	};

	const openCloneStep = () => {
		setError(null);
		setErrorCode(null);
		setValidationScan(null);
		setRepositorySetup(null);
		setSelectedKind("single_repo");
		setModePickerOpen(false);
		setCloneDialogOpen(true);
	};

	const confirmCloneUrl = (url: string) => {
		setError(null);
		setErrorCode(null);
		setCloneUrl(url);
		setCloneDialogOpen(false);
		setPendingCloneUrl(url);
	};

	const chooseDirectory = async (kind: ProjectKind) => {
		setError(null);
		setErrorCode(null);
		setValidationScan(null);
		setRepositorySetup(null);
		setRepositorySetupWarning(null);
		setSelectedKind(kind);
		setIsChoosingPath(true);
		try {
			const path = await aoBridge.app.chooseDirectory(
				kind === "workspace" ? t("createProject.chooseWorkspace") : t("createProject.chooseRepo"),
			);
			if (path && kind === "single_repo") {
				const preflight = await projectRepositoryPreflight(path);
				if (preflight.blockingError) {
					setError(preflight.blockingError);
					setValidationScan(preflight.scan);
					setModePickerOpen(false);
					setFolderPickerOpen(true);
					return;
				}
				setRepositorySetup(preflight.setupCode);
				setRepositorySetupWarning(preflight.setupWarning);
			}
			if (path && kind === "workspace") {
				try {
					const warning = await aoBridge.app.checkAncestorRepo(path);
					if (warning) {
						setRepositorySetupWarning(warning);
						setRepositorySetup("NOT_A_GIT_REPO");
					}
				} catch {
					// Ancestor check failed — proceed without warning
				}
			}
			if (path) {
				setModePickerOpen(false);
				setSelectedPath(path);
				setFolderPickerOpen(false);
			}
		} catch (err) {
			setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsChoosingPath(false);
		}
	};

	const startFlow = () => {
		if (hasModePicker) {
			setError(null);
			setModePickerOpen(true);
			return;
		}
		void chooseDirectory(mode);
	};

	// Seed with the current value so we never open on mount; open when it changes.
	const lastOpenSignal = useRef(openSignal);
	useEffect(() => {
		if (openSignal === undefined || openSignal === lastOpenSignal.current) return;
		lastOpenSignal.current = openSignal;
		startFlow();
	}, [openSignal]);

	const createProject = async (selection: CreateProjectAgentSelection) => {
		const path = selectedPath;
		const clone = pendingCloneUrl;
		if (path === null && clone === null) return;
		setError(null);
		setErrorCode(null);
		setIsCreating(true);
		try {
			if (path !== null && selectedKind === "single_repo" && repositorySetup) {
				setIsCreating(false);
				setIsInitializing(true);
				await onInitializeProject(path);
				setRepositorySetup(null);
				setRepositorySetupWarning(null);
				setIsInitializing(false);
				setIsCreating(true);
			}
			if (clone !== null) {
				await onCreateProject({ cloneUrl: clone, ...selection });
				setPendingCloneUrl(null);
				setCloneUrl("");
			} else if (path !== null) {
				await onCreateProject({ path, asWorkspace: selectedKind === "workspace", ...selection });
				setSelectedPath(null);
			}
		} catch (err) {
			const code = err instanceof Error && "code" in err ? (err.code as string | undefined) : undefined;
			// isRepositorySetupRecoveryCode is checked below, after the clone
			// early-return: a clone failure has no repo-setup recovery UI to show.
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			setError(message);
			setErrorCode(code ?? null);
			if (clone !== null) {
				// Back to the URL step: every clone failure is fixed there, either
				// by editing the URL or by fixing credentials and retrying it.
				setPendingCloneUrl(null);
				setCloneDialogOpen(true);
				return;
			}
			if (selectedKind === "single_repo" && isRepositorySetupRecoveryCode(code)) setRepositorySetup(code);
			if (hasModePicker && selectedPath !== null) {
				if (shouldScanCreateFailure(message)) {
					try {
						const scan = await aoBridge.app.scanImportFolder({
							path: selectedPath,
							mode: selectedKind === "workspace" ? "workspace" : "project",
						});
						setValidationScan(scan);
					} catch {
						setValidationScan({ path: selectedPath, repos: [] });
					}
				} else {
					setValidationScan(null);
				}
				setSelectedPath(null);
				setFolderPickerOpen(true);
			}
		} finally {
			setIsCreating(false);
			setIsInitializing(false);
		}
	};

	const label = isChoosingPath
		? t("createProject.opening")
		: isInitializing
			? hasModePicker
				? t("createProject.initializing")
				: t("createProject.settingUp")
			: isCreating
				? t("createProject.creating")
				: resolvedIdleLabel;

	return (
		<>
			{!embedded &&
				children?.({
					choosePath: startFlow,
					disabled: isBusy,
					error,
					label,
				})}
			{hasModePicker && embedded && !modePickerOpen && (
				<div className="flex w-full flex-col items-center gap-3">
					<ImportModePicker disabled={isBusy} onSelect={openFolderStep} onCloneFromUrl={openCloneStep} />
					{error && !folderPickerOpen && !cloneDialogOpen && selectedPath === null && (
						<p className="text-caption leading-body text-error" role="status">
							{error}
						</p>
					)}
				</div>
			)}
			{hasModePicker && (
				<>
					<CreateProjectModeDialog
						disabled={isBusy}
						open={modePickerOpen}
						onOpenChange={(open) => !isBusy && setModePickerOpen(open)}
						onSelect={openFolderStep}
						onCloneFromUrl={openCloneStep}
					/>
					<CloneProjectDialog
						disabled={isBusy}
						error={error ? cloneErrorPresentation(errorCode ?? undefined, error) : null}
						open={cloneDialogOpen}
						url={cloneUrl}
						onUrlChange={setCloneUrl}
						onBack={() => {
							setError(null);
							setErrorCode(null);
							setCloneDialogOpen(false);
							if (!embedded) {
								window.requestAnimationFrame(() => setModePickerOpen(true));
							}
						}}
						onOpenChange={(open) => {
							if (isBusy) return;
							setCloneDialogOpen(open);
							if (!open) {
								setError(null);
								setErrorCode(null);
							}
						}}
						onSubmit={confirmCloneUrl}
					/>
					<CreateProjectFolderDialog
						disabled={isBusy}
						error={error}
						kind={selectedKind}
						open={folderPickerOpen}
						scan={validationScan}
						onBack={() => {
							setError(null);
							setValidationScan(null);
							setFolderPickerOpen(false);
							if (!embedded) {
								window.requestAnimationFrame(() => setModePickerOpen(true));
							}
						}}
						onChooseFolder={() => void chooseDirectory(selectedKind)}
						onOpenChange={(open) => {
							if (!isBusy) {
								setFolderPickerOpen(open);
								if (!open) {
									setError(null);
									setValidationScan(null);
								}
							}
						}}
					/>
				</>
			)}
			<CreateProjectAgentSheet
				cloneUrl={pendingCloneUrl}
				error={pendingCloneUrl === null ? error : null}
				isCreating={isCreating}
				isInitializing={isInitializing}
				kind={selectedKind}
				onOpenChange={(open) => {
					if (!open) {
						setSelectedPath(null);
						setPendingCloneUrl(null);
						if (!folderPickerOpen && !cloneDialogOpen) {
							setError(null);
							setErrorCode(null);
						}
					}
				}}
				onSubmit={createProject}
				open={selectedPath !== null || pendingCloneUrl !== null}
				path={selectedPath}
				repositorySetupNeeded={repositorySetup !== null}
				repositorySetupWarning={repositorySetupWarning}
			/>
			{error && !hasModePicker && (
				<span className="sr-only" role="status">
					{error}
				</span>
			)}
		</>
	);
}

function isRepositorySetupRecoveryCode(code: string | undefined): code is "NOT_A_GIT_REPO" | "PROJECT_UNBORN" {
	return code === "NOT_A_GIT_REPO" || code === "PROJECT_UNBORN";
}

type RepositorySetupCode = "NOT_A_GIT_REPO" | "PROJECT_UNBORN";

type ProjectRepositoryPreflight = {
	blockingError: string | null;
	scan: ImportFolderScan | null;
	setupCode: RepositorySetupCode | null;
	setupWarning: string | null;
};

async function projectRepositoryPreflight(path: string): Promise<ProjectRepositoryPreflight> {
	try {
		const scan = await aoBridge.app.scanImportFolder({ path, mode: "project" });
		const reason = scan.repos[0]?.reason ?? "";
		if (reason.startsWith("Selected folder is inside AO's internal data directory.")) {
			return {
				blockingError: reason,
				scan,
				setupCode: null,
				setupWarning: null,
			};
		}
		if (scan.repos.length === 0) {
			return { blockingError: null, scan, setupCode: "NOT_A_GIT_REPO", setupWarning: scan.setupWarning ?? null };
		}
		return {
			blockingError: null,
			scan,
			setupCode: reason === "Repository must have at least one commit." ? "PROJECT_UNBORN" : null,
			setupWarning: null,
		};
	} catch {
		return { blockingError: null, scan: null, setupCode: null, setupWarning: null };
	}
}

function shouldScanCreateFailure(message: string): boolean {
	if (/daemon|server|conflict|already exists|not ready|start|orchestrator|permission denied/i.test(message))
		return false;
	if (/\b(?:PATH|ID)_ALREADY_REGISTERED\b/i.test(message) || /already registered/i.test(message)) return false;
	return /workspace|repo|repository|git|path|folder|worktree|bare|branch|commit|remote/i.test(message);
}

function CreateProjectModeDialog({
	disabled,
	onCloneFromUrl,
	onOpenChange,
	onSelect,
	open,
}: {
	disabled: boolean;
	onCloneFromUrl: () => void;
	onOpenChange: (open: boolean) => void;
	onSelect: (kind: ProjectKind) => void;
	open: boolean;
}) {
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(var(--size-import-modal-max),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 border-0 bg-transparent p-0 shadow-none outline-none data-[state=open]:animate-modal-in">
					<ImportModePicker
						disabled={disabled}
						onCloneFromUrl={onCloneFromUrl}
						onClose={() => onOpenChange(false)}
						onSelect={onSelect}
						dialog
					/>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

/** Figma "Dialog - ModalContainer" — Workspace vs Project import chooser. */
function ImportModePicker({
	dialog = false,
	disabled,
	onCloneFromUrl,
	onClose,
	onSelect,
}: {
	dialog?: boolean;
	disabled: boolean;
	onCloneFromUrl: () => void;
	onClose?: () => void;
	onSelect: (kind: ProjectKind) => void;
}) {
	const { t } = useTranslation();
	return (
		<div
			className="relative isolate flex w-full max-w-(--size-import-modal-max) flex-col items-stretch gap-8 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-(--size-import-modal-padding) shadow-[var(--shadow-import-modal)]"
			role={dialog ? undefined : "group"}
			aria-label={dialog ? undefined : t("createProject.importTitle")}
		>
			<div className={cn("relative z-[1] flex flex-col items-start gap-1", onClose && "pr-10")}>
				{dialog ? (
					<Dialog.Title className="import-title">{t("createProject.importTitle")}</Dialog.Title>
				) : (
					<h2 className="import-title">{t("createProject.importTitle")}</h2>
				)}
				{dialog ? (
					<Dialog.Description className="import-description">{t("createProject.importWhat")}</Dialog.Description>
				) : (
					<p className="import-description">{t("createProject.importWhat")}</p>
				)}
			</div>
			<div className="relative z-[2] flex flex-row items-stretch justify-center gap-6 self-stretch">
				<ProjectModeButton
					description={t("createProject.workspaceDesc")}
					disabled={disabled}
					kind="workspace"
					onClick={() => onSelect("workspace")}
				/>
				<ProjectModeButton
					description={t("createProject.projectDesc")}
					disabled={disabled}
					kind="single_repo"
					onClick={() => onSelect("single_repo")}
				/>
			</div>
			{/* Third source, deliberately quieter than the two cards: both of those
			    import something already on this machine, this one fetches it first. */}
			<div className="relative z-[2] flex flex-col items-center gap-1.5 self-stretch border-t border-[var(--color-border-import-modal)] pt-6 text-center">
				<button
					type="button"
					className="inline-flex items-center gap-2 rounded-md px-2 py-1 text-[14px] font-semibold text-[var(--color-text-import-title)] underline-offset-4 transition-colors hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50"
					disabled={disabled}
					onClick={onCloneFromUrl}
				>
					<CloudDownload className="size-4 shrink-0" aria-hidden="true" />
					Clone from a Git URL
				</button>
				<p className="text-[13px] font-normal leading-5 text-[var(--color-text-import-muted)]">
					Not on this machine yet? AO clones the repository first, then imports it as a project.
				</p>
			</div>
			{onClose && (
				<button
					type="button"
					className="import-close-button"
					aria-label={t("createProject.closeDialog")}
					disabled={disabled}
					onClick={onClose}
				>
					<X className="size-5" aria-hidden="true" strokeWidth={1.67} />
				</button>
			)}
		</div>
	);
}

function ProjectModeButton({
	description,
	disabled,
	kind,
	onClick,
}: {
	description: string;
	disabled: boolean;
	kind: ProjectKind;
	onClick: () => void;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const title = isWorkspace ? t("createProject.workspace") : t("createProject.project");
	return (
		<button
			type="button"
			aria-label={title}
			className="flex min-h-(--size-import-mode-card-min) w-full flex-1 flex-col justify-start gap-6 self-stretch rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-6 text-left transition-colors hover:bg-[var(--color-bg-import-card-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50 sm:min-h-(--size-import-mode-card-min-sm)"
			disabled={disabled}
			onClick={onClick}
		>
			<span className="flex w-full flex-col items-start">
				<span
					className={cn(
						"flex h-(--size-import-mode-illustration) w-full justify-center",
						isWorkspace ? "items-start" : "items-center",
					)}
				>
					{isWorkspace ? (
						<span className="flex h-(--size-import-mode-illustration) w-full max-w-[240px] flex-col items-start gap-3 rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-illustration)] p-4">
							<span className="flex items-center gap-2 text-[14px] leading-5 text-[var(--color-text-import-muted)]">
								<Folder className="size-[14px] shrink-0" aria-hidden="true" />
								my-workspace/
							</span>
							<span className="flex w-full flex-col items-start gap-2">
								{["web-app", "api-server", "shared-libs"].map((repo) => (
									<span key={repo} className="flex w-full items-center px-3 py-2">
										<span className="mr-2 size-2 shrink-0 rounded-full bg-accent-strong" aria-hidden="true" />
										<span className="text-[12px] font-bold leading-4 text-[var(--color-text-import-title)]">
											{repo}
										</span>
									</span>
								))}
							</span>
						</span>
					) : (
						<span className="flex h-[50px] w-fit items-center rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-chip)] px-4 py-3">
							<span className="mr-2 size-2 shrink-0 rounded-full bg-accent-strong" aria-hidden="true" />
							<span className="text-[14px] font-bold leading-5 text-[var(--color-text-import-title)]">web-app</span>
							<span className="px-1 text-[16px] leading-6 text-[var(--color-text-import-muted)]" aria-hidden="true">
								·
							</span>
							<span className="text-[14px] font-normal leading-5 text-[var(--color-text-import-muted)]">main</span>
						</span>
					)}
				</span>
			</span>
			<span className="mt-auto flex w-full flex-col items-start gap-2">
				<span className="text-[16px] font-bold leading-6 text-[var(--color-text-import-title)]">
					{title}
				</span>
				<span className="text-[14px] font-normal leading-[23px] text-[var(--color-text-import-muted)]">
					{description}
				</span>
			</span>
		</button>
	);
}

function CreateProjectFolderDialog({
	disabled,
	error,
	kind,
	onBack,
	onChooseFolder,
	onOpenChange,
	open,
	scan,
}: {
	disabled: boolean;
	error: string | null;
	kind: ProjectKind;
	onBack: () => void;
	onChooseFolder: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	scan: ImportFolderScan | null;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const failedRepos = scan?.repos.filter((repo) => repo.status === "error" || !repo.hasRemote) ?? [];
	const hasScan = scan !== null;
	const footerMessage =
		failedRepos.length > 0
			? t("createProject.footerResolve", { count: failedRepos.length })
			: hasScan
				? t("createProject.footerReview")
				: t("createProject.footerChoose");
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(var(--size-import-folder-dialog),calc(100svh-24px))] w-[min(var(--size-import-folder-dialog),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-0 text-[var(--color-text-import-title)] shadow-[var(--shadow-import-modal)] data-[state=open]:animate-modal-in">
					<div className="flex shrink-0 items-start gap-3 border-b border-[var(--color-border-import-modal)] p-(--size-import-dialog-padding) sm:gap-4">
						<Button
							type="button"
							variant="outline"
							size="icon"
							aria-label={t("createProject.backToType")}
							disabled={disabled}
							onClick={onBack}
						>
							<ChevronRight className="size-4 rotate-180" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1">
							<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{isWorkspace ? t("createProject.importWorkspace") : t("createProject.importProject")}
							</Dialog.Title>
							<Dialog.Description className="mt-1 max-w-[520px] text-[13px] font-medium leading-5 text-[var(--color-text-import-muted)]">
								{isWorkspace ? t("createProject.importWorkspaceDesc") : t("createProject.importProjectDesc")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button
								type="button"
								className="settings-close-button"
								aria-label={t("createProject.closeImport")}
								disabled={disabled}
							>
								<X className="size-4" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<div className="min-h-0 overflow-y-auto p-(--size-import-dialog-padding)">
						{hasScan ? (
							<div className="space-y-4">
								<div className="flex items-center gap-3 rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-4">
									<Folder className="size-5 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
									<div className="min-w-0 flex-1">
										<div className="truncate font-mono text-[14px] font-semibold text-[var(--color-text-import-title)]">
											{displayImportPath(scan.path)}
										</div>
										<div className="mt-0.5 text-[12px] text-[var(--color-text-import-muted)]">
											{isWorkspace ? t("createProject.workspaceRoot") : t("createProject.projectFolder")}
										</div>
									</div>
									<Button type="button" variant="footer" disabled={disabled} onClick={onChooseFolder}>
										{t("createProject.change")}
									</Button>
								</div>

								{error && (
									<div className="rounded-lg border border-destructive/40 bg-destructive/10">
										<div className="border-b border-destructive/30 px-4 py-3 font-mono text-[12px] font-semibold uppercase tracking-[0.12em] text-destructive">
											<span className="mr-2 inline-block size-2 rounded-full bg-destructive" aria-hidden="true" />
											{isWorkspace ? t("createProject.importFailedWorkspace") : t("createProject.importFailedProject")}
										</div>
										<div className="px-4 py-3 text-[12px] leading-5 text-destructive">{error}</div>
										{failedRepos.length > 0 && (
											<div className="border-t border-destructive/30">
												{failedRepos.map((repo) => (
													<ImportRepoRow key={repo.path} repo={repo} failed />
												))}
											</div>
										)}
									</div>
								)}

								{scan.repos
									.filter((repo) => repo.status !== "error" && repo.hasRemote)
									.map((repo) => (
										<div
											key={repo.path}
											className="rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)]"
										>
											<ImportRepoRow repo={repo} />
										</div>
									))}

								{scan.repos.length === 0 && (
									<div className="rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-4 text-[12px] text-[var(--color-text-import-muted)]">
										{t("createProject.noRepos")}
									</div>
								)}
							</div>
						) : (
							<button
								type="button"
								className="flex min-h-[132px] w-full flex-col items-center justify-center rounded-lg border border-dashed border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-6 text-center transition-colors hover:bg-[var(--color-bg-import-card-hover)] disabled:pointer-events-none disabled:opacity-50 sm:min-h-[160px]"
								disabled={disabled}
								onClick={onChooseFolder}
							>
								<span className="mb-4 grid size-11 place-items-center rounded-xl bg-[var(--color-bg-import-chip)] text-[var(--color-text-import-muted)]">
									<FolderPlus className="size-5" aria-hidden="true" />
								</span>
								<span className="text-[15px] font-semibold text-[var(--color-text-import-title)]">
									{isWorkspace ? t("createProject.chooseFolder") : t("createProject.chooseProjectFolder")}
								</span>
								<span className="mt-2 max-w-full text-pretty text-[12px] text-[var(--color-text-import-muted)] sm:text-[13px]">
									{isWorkspace ? t("createProject.pickerWorkspaceHint") : t("createProject.pickerProjectHint")}
								</span>
							</button>
						)}
						{error && !hasScan && (
							<div
								className={cn(
									"mt-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-3 text-[12px] leading-5 text-destructive",
								)}
							>
								{error}
							</div>
						)}
					</div>
					<div className="flex shrink-0 flex-col gap-3 border-t border-[var(--color-border-import-modal)] p-(--size-import-dialog-padding) sm:flex-row sm:items-center sm:justify-between">
						<p className="text-[12px] font-medium text-[var(--color-text-import-muted)]">{footerMessage}</p>
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="footer" disabled={disabled} onClick={() => onOpenChange(false)}>
								{t("createProject.cancel")}
							</Button>
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

/**
 * Clone-by-URL step, and the surface every clone failure comes back to: the
 * daemon's remediation always resolves here, either by editing the URL or by
 * fixing credentials on the machine and submitting the same URL again.
 */
function CloneProjectDialog({
	disabled,
	error,
	onBack,
	onOpenChange,
	onSubmit,
	onUrlChange,
	open,
	url,
}: {
	disabled: boolean;
	error: { title: string; message: string } | null;
	onBack: () => void;
	onOpenChange: (open: boolean) => void;
	onSubmit: (url: string) => void;
	onUrlChange: (url: string) => void;
	open: boolean;
	url: string;
}) {
	// Validity is checked as you type, but only reported once the field has been
	// left or a submit was attempted: no red text while typing a valid URL.
	const [touched, setTouched] = useState(false);
	useEffect(() => {
		if (!open) setTouched(false);
	}, [open]);

	const target = parseCloneUrl(url);
	const invalid = touched && url.trim() !== "" && target === null;

	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[calc(100svh-24px)] w-[min(var(--size-import-folder-dialog),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-0 text-[var(--color-text-import-title)] shadow-[var(--shadow-import-modal)] data-[state=open]:animate-modal-in">
					<form
						className="flex min-h-0 flex-col"
						onSubmit={(event) => {
							event.preventDefault();
							setTouched(true);
							if (!disabled && target) onSubmit(url.trim());
						}}
					>
						<div className="flex shrink-0 items-start gap-3 border-b border-[var(--color-border-import-modal)] px-4 py-4 sm:gap-4 sm:px-6 sm:py-5">
							<button
								type="button"
								className="grid size-8 shrink-0 place-items-center rounded-lg border border-[var(--color-border-import-modal)] text-[var(--color-text-import-muted)] transition hover:bg-[var(--color-bg-import-card-hover)] hover:text-[var(--color-text-import-title)] disabled:pointer-events-none disabled:opacity-50"
								aria-label="Back to import type"
								disabled={disabled}
								onClick={onBack}
							>
								<ChevronRight className="size-4 rotate-180" aria-hidden="true" />
							</button>
							<div className="min-w-0 flex-1">
								<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
									Clone a repository
								</Dialog.Title>
								<Dialog.Description className="mt-1 max-w-[440px] text-[13px] font-medium leading-5 text-[var(--color-text-import-muted)]">
									AO clones the repository onto the machine running it, then imports the clone as a project.
								</Dialog.Description>
							</div>
							<Dialog.Close asChild>
								<button
									type="button"
									className="grid size-7 shrink-0 place-items-center rounded-md text-[var(--color-text-import-muted)] transition hover:bg-[var(--color-bg-import-card-hover)] hover:text-[var(--color-text-import-title)] disabled:pointer-events-none disabled:opacity-50"
									aria-label="Close clone dialog"
									disabled={disabled}
								>
									<X className="size-4" aria-hidden="true" />
								</button>
							</Dialog.Close>
						</div>

						<div className="min-h-0 space-y-4 overflow-y-auto px-4 py-4 sm:px-6 sm:py-6">
							<div className="flex flex-col gap-1.5">
								<Label
									htmlFor="cloneProjectUrl"
									className="text-[12px] font-medium text-[var(--color-text-import-muted)]"
								>
									Repository URL
								</Label>
								<Input
									id="cloneProjectUrl"
									autoFocus
									autoComplete="off"
									autoCorrect="off"
									autoCapitalize="off"
									spellCheck={false}
									aria-invalid={invalid || undefined}
									aria-describedby="cloneProjectUrlHint"
									className="font-mono text-[13px]"
									disabled={disabled}
									placeholder="https://github.com/owner/repo.git"
									value={url}
									onBlur={() => setTouched(true)}
									onChange={(event) => onUrlChange(event.target.value)}
								/>
								<p
									id="cloneProjectUrlHint"
									className={cn(
										"text-[12px] leading-5",
										invalid ? "text-destructive" : "text-[var(--color-text-import-muted)]",
									)}
								>
									{invalid
										? "Use an https:// or ssh git remote URL that names an owner and a repository."
										: "https:// or ssh, for example git@github.com:owner/repo.git"}
								</p>
							</div>

							{/* Standing expectation-setting, dropped once a real failure is on
							    screen: the daemon's own remediation supersedes it. */}
							{!error && (
								<div className="rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-4 py-3 text-[12px] leading-5 text-[var(--color-text-import-muted)]">
									A large repository can take a few minutes to clone. A private one needs git credentials on that
									machine: run <RemediationText text="`gh auth login`" /> there first.
								</div>
							)}

							{error && (
								<div
									role="alert"
									className="flex gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-[12px] leading-5"
								>
									<TriangleAlert className="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden="true" />
									<div className="min-w-0 space-y-1">
										<p className="font-semibold text-destructive">{error.title}</p>
										<p className="text-[var(--color-text-import-muted)]">
											<RemediationText text={error.message} />
										</p>
									</div>
								</div>
							)}
						</div>

						<div className="flex shrink-0 flex-col gap-3 border-t border-[var(--color-border-import-modal)] px-4 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-6">
							<p className="text-[12px] font-medium text-[var(--color-text-import-muted)]">
								{target ? `Clones ${target.owner}/${target.repo}` : "Paste the repository's git remote URL"}
							</p>
							<div className="flex flex-wrap items-center justify-end gap-2 sm:gap-3">
								<Button type="button" variant="outline" disabled={disabled} onClick={() => onOpenChange(false)}>
									Cancel
								</Button>
								<Button type="submit" variant="primary" disabled={disabled || target === null}>
									Continue
								</Button>
							</div>
						</div>
					</form>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

/**
 * The daemon writes its remediation with the command in backticks ("run `gh
 * auth login`"). Render those spans as code so the thing to copy is obvious.
 */
export function RemediationText({ text }: { text: string }) {
	return (
		<>
			{text.split(/`([^`]+)`/).map((part, index) =>
				index % 2 === 1 ? (
					<code
						key={`${index}:${part}`}
						className="rounded bg-[var(--color-bg-import-chip)] px-1 py-0.5 font-mono text-[11px] text-[var(--color-text-import-title)]"
					>
						{part}
					</code>
				) : (
					part
				),
			)}
		</>
	);
}

function ImportRepoRow({ failed = false, repo }: { failed?: boolean; repo: ImportFolderScan["repos"][number] }) {
	const { t } = useTranslation();
	return (
		<div className="flex items-center gap-3 p-4">
			{failed ? (
				<XCircle className="size-5 shrink-0 text-destructive" aria-hidden="true" />
			) : (
				<CheckCircle2 className="size-5 shrink-0 text-success" aria-hidden="true" />
			)}
			<div className="min-w-0 flex-1">
				<div className="truncate text-[14px] font-semibold text-[var(--color-text-import-title)]">{repo.name}</div>
				<div className="mt-0.5 truncate font-mono text-[12px] text-[var(--color-text-import-muted)]">
					{displayImportPath(repo.path)}
				</div>
			</div>
			<div className="hidden max-w-[260px] shrink-0 truncate text-right font-mono text-[12px] text-[var(--color-text-import-muted)] sm:block">
				{failed ? (repo.reason ?? t("createProject.repoCannotImport")) : `${repo.branch} ${remoteDisplay(repo.remote)}`}
			</div>
		</div>
	);
}

function displayImportPath(value: string) {
	return value.replace(/^\/Users\/[^/]+/, "~");
}

function remoteDisplay(remote: string) {
	const ssh = remote.match(/^[^@]+@([^:]+):(.+)$/);
	if (ssh?.[1] && ssh[2]) return `${ssh[1]}/${ssh[2].replace(/\.git$/, "")}`;
	try {
		const url = new URL(remote);
		return `${url.host}${url.pathname.replace(/\.git$/, "")}`;
	} catch {
		return remote.replace(/^https?:\/\//, "").replace(/\.git$/, "");
	}
}
