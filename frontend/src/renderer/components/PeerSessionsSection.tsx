import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { AlertTriangle, GitBranch, Loader2 } from "lucide-react";
import { usePeerWorkspacesQuery } from "../hooks/usePeerWorkspacesQuery";
import { switchToMachine } from "../lib/peer-session-switch";
import { formatTimeCompact } from "../lib/format-time";
import { getSessionStatusView } from "../lib/session-presentation";
import { cn } from "../lib/utils";
import { aoBridge } from "../lib/bridge";
import { toAgentProvider, toSessionStatus } from "../types/workspace";
import type { PeerProject, PeerSession, PeerWorkspacesResult } from "../../shared/peer-workspaces";
import { AgentAvatar } from "./AgentAvatar";
import { aoMachinesQueryKey } from "./settings/MachinesSection";

type SectionRole = "CLOUD" | "LOCAL";

type RowSwitchState =
	| { phase: "idle" }
	| { phase: "switching"; sessionId: string }
	| { phase: "error"; sessionId: string; message: string };

/**
 * "Peer" (the daemon that is NOT active) can be either the cloud machine or
 * this computer, depending on which one is currently active. This resolves
 * which section the peer plays and which the active board plays, then
 * renders both in the same card presentation, CLOUD first.
 */
export function CloudLocalSections({ activeBoardContent }: { activeBoardContent: ReactNode }) {
	const { t } = useTranslation();
	const peerQuery = usePeerWorkspacesQuery(true);
	const activeMachineName = useActiveMachineName();
	const [switchState, setSwitchState] = useState<RowSwitchState>({ phase: "idle" });
	const navigate = useNavigate();
	const result = peerQuery.data;
	// Default to "peer is cloud" (active is local) while unresolved: that is
	// the common case (this computer is machine zero, always active by
	// default) and it is what a demo hits before any registered machine
	// answers a probe.
	const peerIsRemote = result?.state === "ok" ? result.isRemote : true;
	const peerRole: SectionRole = peerIsRemote ? "CLOUD" : "LOCAL";
	const activeRole: SectionRole = peerIsRemote ? "LOCAL" : "CLOUD";

	const openPeerSession = async (session: PeerSession, projectId: string, machineId: string) => {
		if (switchState.phase === "switching") return;
		setSwitchState({ phase: "switching", sessionId: session.id });
		const outcome = await switchToMachine(machineId);
		if (outcome.status === "error") {
			setSwitchState({ phase: "error", sessionId: session.id, message: outcome.message });
			return;
		}
		setSwitchState({ phase: "idle" });
		void navigate({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId, sessionId: session.id },
		});
	};

	const peerSection = (
		<PeerSection
			role={peerRole}
			query={peerQuery}
			result={result}
			switchState={switchState}
			onOpen={openPeerSession}
		/>
	);
	const activeSection = (
		<section
			aria-label={activeRole === "CLOUD" ? t("peerSessions.cloudSessionsLabel") : t("peerSessions.localSessionsLabel")}
			className="flex min-h-0 flex-1 flex-col gap-2"
			data-testid={activeRole === "CLOUD" ? "board-section-cloud" : "board-section-local"}
		>
			<SectionHeading kind={activeRole} machineName={activeMachineName} />
			<div className="min-h-0 flex-1 overflow-hidden">{activeBoardContent}</div>
		</section>
	);

	return (
		<div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto">
			{peerRole === "CLOUD" ? peerSection : activeSection}
			{peerRole === "CLOUD" ? activeSection : peerSection}
		</div>
	);
}

function useActiveMachineName(): string | undefined {
	const query = useQuery({ queryKey: aoMachinesQueryKey, queryFn: () => aoBridge.machines.refresh() });
	const state = query.data;
	return state?.machines.find((machine) => machine.id === state.activeMachineId)?.name;
}

function PeerSection({
	role,
	query,
	result,
	switchState,
	onOpen,
}: {
	role: SectionRole;
	query: UseQueryResult<PeerWorkspacesResult>;
	result: PeerWorkspacesResult | undefined;
	switchState: RowSwitchState;
	onOpen: (session: PeerSession, projectId: string, machineId: string) => void;
}) {
	const { t } = useTranslation();
	return (
		<section
			aria-label={role === "CLOUD" ? t("peerSessions.cloudSessionsLabel") : t("peerSessions.localSessionsLabel")}
			className="flex min-h-0 flex-col gap-2"
			data-testid={role === "CLOUD" ? "board-section-cloud" : "board-section-local"}
		>
			<SectionHeading kind={role} machineName={result?.state === "ok" ? result.machineName : undefined} />
			{query.isLoading ? (
				<PeerStatusMessage
					icon={Loader2}
					spin
					text={role === "CLOUD" ? t("peerSessions.lookingForCloud") : t("peerSessions.lookingForLocal")}
				/>
			) : query.isError || !result ? (
				<PeerStatusMessage
					icon={AlertTriangle}
					text={role === "CLOUD" ? t("peerSessions.couldNotLoadCloud") : t("peerSessions.couldNotLoadLocal")}
				/>
			) : result.state === "unavailable" ? (
				<PeerStatusMessage icon={AlertTriangle} text={result.reason} />
			) : result.projects.every((project) => project.sessions.length === 0) ? (
				<PeerStatusMessage text={role === "CLOUD" ? t("peerSessions.noCloudSessions") : t("peerSessions.noLocalSessions")} />
			) : (
				<div className="board-scrollbar min-h-0 flex-1 overflow-y-auto">
					<div className="flex flex-col gap-3 pb-2">
						{result.projects
							.filter((project) => project.sessions.length > 0)
							.map((project) => (
								<PeerProjectGroup
									key={project.id}
									machineId={result.machineId}
									machineName={result.machineName}
									project={project}
									onOpen={onOpen}
									switchState={switchState}
								/>
							))}
					</div>
				</div>
			)}
		</section>
	);
}

function SectionHeading({ kind, machineName }: { kind: SectionRole; machineName?: string }) {
	return (
		<div className="flex shrink-0 items-baseline gap-2 px-1">
			<span className="font-mono text-2xs font-medium uppercase tracking-wide-sm text-passive">{kind}</span>
			{machineName ? <span className="truncate text-caption text-muted-foreground">{machineName}</span> : null}
		</div>
	);
}

function PeerStatusMessage({
	icon: Icon,
	text,
	spin,
}: {
	icon?: typeof AlertTriangle;
	text: string;
	spin?: boolean;
}) {
	return (
		<div className="flex items-center gap-2 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
			{Icon ? <Icon className={cn("size-icon-base shrink-0", spin && "animate-spin")} aria-hidden="true" /> : null}
			<span>{text}</span>
		</div>
	);
}

function PeerProjectGroup({
	project,
	machineId,
	machineName,
	onOpen,
	switchState,
}: {
	project: PeerProject;
	machineId: string;
	machineName: string;
	onOpen: (session: PeerSession, projectId: string, machineId: string) => void;
	switchState: RowSwitchState;
}) {
	// Only worker sessions get a card on the board too; the orchestrator is
	// reached through its own button, not a board row.
	const sessions = project.sessions.filter((session) => session.kind !== "orchestrator");
	if (sessions.length === 0) return null;

	return (
		<div className="flex flex-col gap-1.5" data-testid="peer-project-group">
			<span className="truncate px-1 text-2xs font-medium text-passive">{project.name}</span>
			<div className="flex flex-col gap-2">
				{sessions.map((session) => (
					<PeerSessionRow
						key={session.id}
						session={session}
						projectId={project.id}
						machineId={machineId}
						machineName={machineName}
						onOpen={onOpen}
						switchState={switchState}
					/>
				))}
			</div>
		</div>
	);
}

function PeerSessionRow({
	session,
	projectId,
	machineId,
	machineName,
	onOpen,
	switchState,
}: {
	session: PeerSession;
	projectId: string;
	machineId: string;
	machineName: string;
	onOpen: (session: PeerSession, projectId: string, machineId: string) => void;
	switchState: RowSwitchState;
}) {
	const { t } = useTranslation();
	const status = toSessionStatus(session.status);
	const badge = getSessionStatusView(status);
	const provider = toAgentProvider(session.harness);
	const isSwitchingThis = switchState.phase === "switching" && switchState.sessionId === session.id;
	const isBlocked = switchState.phase === "switching" && !isSwitchingThis;
	const rowError = switchState.phase === "error" && switchState.sessionId === session.id ? switchState.message : undefined;

	return (
		<div
			aria-disabled={isBlocked || isSwitchingThis}
			className={cn(
				"group relative w-full rounded-lg border text-left transition-[border-color,box-shadow]",
				badge.cardClassName ?? "border-border bg-surface",
				!isBlocked && !isSwitchingThis && "cursor-pointer hover:border-border-strong hover:shadow-sm",
				isBlocked && "opacity-60",
			)}
			data-testid="peer-session-row"
			data-session-id={session.id}
			onClick={() => {
				if (isBlocked || isSwitchingThis) return;
				onOpen(session, projectId, machineId);
			}}
			onKeyDown={(event) => {
				if (event.key !== "Enter" && event.key !== " ") return;
				if (isBlocked || isSwitchingThis) return;
				event.preventDefault();
				onOpen(session, projectId, machineId);
			}}
			role="button"
			tabIndex={isBlocked ? -1 : 0}
		>
			<div className="flex items-start gap-2.5 px-3.5 pb-2.5 pt-3">
				<AgentAvatar className="mt-0.5" provider={provider} />
				<div className="min-w-0 flex-1">
					<div
						className="line-clamp-2 overflow-hidden text-sm-md font-semibold leading-tight tracking-tight text-foreground"
						title={session.title}
					>
						{session.title}
					</div>
					{session.branch ? (
						<div className="mt-1.5 flex min-w-0 items-center gap-1.5 font-mono text-2xs text-passive">
							<GitBranch aria-hidden="true" className="size-icon-2xs shrink-0" />
							<span className="truncate">{session.branch}</span>
						</div>
					) : null}
				</div>
			</div>
			<div aria-hidden="true" className="mx-3.5 my-px h-px bg-border" />
			<div className="flex flex-col gap-1.5 px-3.5 py-2">
				<div className="flex items-center justify-between gap-2">
					<span className={cn("inline-flex min-w-0 items-center gap-1.5 truncate text-2xs font-medium", badge.className)}>
						<span className="size-dot-sm shrink-0 rounded-full bg-current" />
						{badge.label}
					</span>
					{session.updatedAt ? (
						<span
							className="shrink-0 whitespace-nowrap font-mono text-2xs text-passive"
							title={t("peerSessions.updatedAt", { time: session.updatedAt })}
						>
							{formatTimeCompact(session.updatedAt)}
						</span>
					) : null}
				</div>
				{isSwitchingThis ? (
					<span className="flex items-center gap-1.5 text-2xs text-muted-foreground" data-testid="peer-switch-progress">
						<Loader2 className="size-icon-2xs animate-spin" aria-hidden="true" />
						{t("peerSessions.switchingTo", { machineName })}
					</span>
				) : null}
				{rowError ? (
					<span className="flex items-start gap-1.5 text-2xs text-destructive" data-testid="peer-switch-error" role="alert">
						<AlertTriangle className="mt-0.5 size-icon-2xs shrink-0" aria-hidden="true" />
						{rowError}
					</span>
				) : null}
			</div>
		</div>
	);
}
