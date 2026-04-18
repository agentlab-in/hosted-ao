"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import dynamic from "next/dynamic";
import { useSearchParams } from "next/navigation";
import { cn } from "@/lib/cn";
import { getSessionTitle } from "@/lib/format";
import { useMediaQuery, MOBILE_BREAKPOINT } from "@/hooks/useMediaQuery";
import type { ProjectInfo } from "@/lib/project-name";
import {
  type DashboardSession,
  NON_RESTORABLE_STATUSES,
  getActivitySignalLabel,
  isDashboardRuntimeEnded,
  isDashboardSessionRestorable,
} from "@/lib/types";
import { MobileBottomNav } from "./MobileBottomNav";
import { ProjectSidebar } from "./ProjectSidebar";
import { SessionDetailPRCard } from "./SessionDetailPRCard";
import { SessionTopStrip } from "./SessionDetailTopStrip";
import { SessionReportAuditPanel } from "./SessionReportAuditPanel";
import { SessionTruthPanel } from "./SessionTruthPanel";
import { askAgentToFix } from "./session-detail-agent-actions";
import {
  activityToneClass,
  ciToneClass,
  formatTimeCompact,
  getCiShortLabel,
  getReviewShortLabel,
  mobileStatusPillClass,
  sessionActivityMeta,
} from "./session-detail-utils";

const DirectTerminal = dynamic(
  () => import("./DirectTerminal").then((module) => ({ default: module.DirectTerminal })),
  {
    ssr: false,
    loading: () => (
      <div className="h-[440px] animate-pulse rounded bg-[var(--color-bg-primary)]" />
    ),
  },
);

interface OrchestratorZones {
  merge: number;
  respond: number;
  review: number;
  pending: number;
  working: number;
  done: number;
}

interface SessionDetailProps {
  session: DashboardSession;
  isOrchestrator?: boolean;
  orchestratorZones?: OrchestratorZones;
  projectOrchestratorId?: string | null;
  projects?: ProjectInfo[];
  sidebarSessions?: DashboardSession[] | null;
  sidebarLoading?: boolean;
  sidebarError?: boolean;
  onRetrySidebar?: () => void;
}

export function SessionDetail({
  session,
  isOrchestrator = false,
  orchestratorZones,
  projectOrchestratorId = null,
  projects = [],
  sidebarSessions = [],
  sidebarLoading = false,
  sidebarError = false,
  onRetrySidebar,
}: SessionDetailProps) {
  const searchParams = useSearchParams();
  const isMobile = useMediaQuery(MOBILE_BREAKPOINT);
  const startFullscreen = searchParams.get("fullscreen") === "true";
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [showTerminal, setShowTerminal] = useState(false);
  const pr = isOrchestrator ? null : session.pr;
  const terminalEnded = isDashboardRuntimeEnded(session);
  const isRestorable =
    !isOrchestrator &&
    isDashboardSessionRestorable(session) &&
    !NON_RESTORABLE_STATUSES.has(session.status);
  const activity = (session.activity && sessionActivityMeta[session.activity]) ?? {
    label: getActivitySignalLabel(session),
    color: "var(--color-text-muted)",
  };
  const headline = getSessionTitle(session);
  const terminalVariant = isOrchestrator ? "orchestrator" : "agent";
  const terminalHeight = isOrchestrator
    ? "clamp(400px, 52vh, 620px)"
    : "clamp(380px, 48vh, 560px)";
  const terminalHeightClass = isOrchestrator
    ? "session-detail-height--orchestrator"
    : "session-detail-height--worker";
  const isOpenCodeSession = session.metadata["agent"] === "opencode";
  const opencodeSessionId =
    typeof session.metadata["opencodeSessionId"] === "string" &&
    session.metadata["opencodeSessionId"].length > 0
      ? session.metadata["opencodeSessionId"]
      : undefined;
  const reloadCommand = opencodeSessionId
    ? `/exit\nopencode --session ${opencodeSessionId}\n`
    : undefined;
  const dashboardHref = session.projectId ? `/?project=${encodeURIComponent(session.projectId)}` : "/";
  const prsHref = session.projectId ? `/prs?project=${encodeURIComponent(session.projectId)}` : "/prs";
  const headerProjectLabel =
    projects.find((project) => project.id === session.projectId)?.name ?? session.projectId;
  const showHeaderProjectLabel = headerProjectLabel.trim().toLowerCase() !== "agent orchestrator";
  const orchestratorHref = useMemo(() => {
    if (isOrchestrator) return `/sessions/${encodeURIComponent(session.id)}`;
    if (!projectOrchestratorId) return null;
    return `/sessions/${encodeURIComponent(projectOrchestratorId)}`;
  }, [isOrchestrator, projectOrchestratorId, session.id]);

  const handleKill = useCallback(async () => {
    try {
      const response = await fetch(`/api/sessions/${encodeURIComponent(session.id)}/kill`, {
        method: "POST",
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      window.location.reload();
    } catch (error) {
      console.error("Failed to kill session:", error);
    }
  }, [session.id]);

  const handleRestore = useCallback(async () => {
    try {
      const response = await fetch(`/api/sessions/${encodeURIComponent(session.id)}/restore`, {
        method: "POST",
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      window.location.reload();
    } catch (error) {
      console.error("Failed to restore session:", error);
    }
  }, [session.id]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => setShowTerminal(true));
    return () => {
      window.cancelAnimationFrame(frame);
      setShowTerminal(false);
    };
  }, [session.id]);

  if (!isMobile) {
    return (
      <div className="dashboard-app-shell">
        <header className="dashboard-app-header">
          {projects.length > 0 ? (
            <button
              type="button"
              className="dashboard-app-sidebar-toggle"
              onClick={() => setSidebarCollapsed((current) => !current)}
              aria-label="Toggle sidebar"
            >
              <svg
                width="14"
                height="14"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.75"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <rect x="3" y="3" width="18" height="18" rx="2" />
                <path d="M9 3v18" />
              </svg>
            </button>
          ) : null}
          <div className="dashboard-app-header__brand">
            <span>Agent Orchestrator</span>
          </div>
          {showHeaderProjectLabel ? (
            <>
              <span className="dashboard-app-header__sep" aria-hidden="true" />
              <span className="dashboard-app-header__project">{headerProjectLabel}</span>
            </>
          ) : null}
          <div className="dashboard-app-header__spacer" />
          <div className="dashboard-app-header__actions">
            {!isOrchestrator && orchestratorHref ? (
              <a
                href={orchestratorHref}
                className="dashboard-app-btn dashboard-app-btn--amber"
                aria-label="Orchestrator"
              >
                <svg
                  width="12"
                  height="12"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <circle cx="12" cy="5" r="2" fill="currentColor" stroke="none" />
                  <path d="M12 7v4M12 11H6M12 11h6M6 11v3M12 11v3M18 11v3" />
                  <circle cx="6" cy="17" r="2" />
                  <circle cx="12" cy="17" r="2" />
                  <circle cx="18" cy="17" r="2" />
                </svg>
                Orchestrator
              </a>
            ) : null}
          </div>
        </header>

        <div
          className={`dashboard-shell dashboard-shell--desktop${sidebarCollapsed ? " dashboard-shell--sidebar-collapsed" : ""}`}
        >
          {projects.length > 0 ? (
            <ProjectSidebar
              projects={projects}
              sessions={sidebarSessions}
              loading={sidebarLoading}
              error={sidebarError}
              onRetry={onRetrySidebar}
              activeProjectId={session.projectId}
              activeSessionId={session.id}
              collapsed={sidebarCollapsed}
              onToggleCollapsed={() => setSidebarCollapsed((current) => !current)}
            />
          ) : null}

          <div className="dashboard-main dashboard-main--desktop">
            <main className="session-detail-page min-h-0 flex-1 overflow-y-auto bg-[var(--color-bg-base)]">
              <div className="session-detail-layout">
                <main className="min-w-0">
                  {(!isOrchestrator || orchestratorZones) && (
                    <SessionTopStrip
                      headline={headline}
                      crumbId={session.id}
                      activityLabel={activity.label}
                      activityColor={activity.color}
                      branch={isOrchestrator ? null : session.branch}
                      pr={pr}
                      isOrchestrator={isOrchestrator}
                      crumbHref={dashboardHref}
                      crumbLabel="Dashboard"
                      onKill={isOrchestrator || terminalEnded ? undefined : handleKill}
                      onRestore={isOrchestrator || !isRestorable ? undefined : handleRestore}
                    />
                  )}

                  {!isOrchestrator && pr ? (
                    <section id="session-pr-section" className="session-detail-pr-section">
                      <SessionDetailPRCard
                        pr={pr}
                        metadata={session.metadata}
                        lifecyclePrReason={session.lifecycle?.prReason}
                        onAskAgentToFix={(comment, onSuccess, onError) =>
                          askAgentToFix(session.id, comment, onSuccess, onError)
                        }
                      />
                    </section>
                  ) : null}

                  {!isOrchestrator ? <SessionTruthPanel session={session} /> : null}
                  {!isOrchestrator ? (
                    <SessionReportAuditPanel
                      sessionId={session.id}
                      entries={session.agentReportAudit ?? []}
                    />
                  ) : null}

                  <section className="session-detail-terminal-wrap">
                    <div id="session-terminal-section" aria-hidden="true" />
                    <div className="session-detail-section-label">
                      <div
                        className={cn(
                          "session-detail-section-label__bar",
                          isOrchestrator
                            ? "session-detail-tone--accent"
                            : activityToneClass(activity.color),
                        )}
                      />
                      <span className="session-detail-section-label__text">Live Terminal</span>
                    </div>
                    {!showTerminal ? (
                      <div
                        className={cn(
                          "session-detail-terminal-placeholder",
                          terminalHeightClass,
                        )}
                      />
                    ) : terminalEnded ? (
                      <div className={cn("terminal-exited-placeholder", terminalHeightClass)}>
                        <span className="terminal-exited-placeholder__text">
                          Terminal session has ended
                        </span>
                      </div>
                    ) : (
                      <DirectTerminal
                        sessionId={session.id}
                        startFullscreen={startFullscreen}
                        variant={terminalVariant}
                        appearance="dark"
                        height={terminalHeight}
                        isOpenCodeSession={isOpenCodeSession}
                        reloadCommand={isOpenCodeSession ? reloadCommand : undefined}
                      />
                    )}
                  </section>
                </main>
              </div>
            </main>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="session-detail--terminal-first">
      <div className="session-detail__floating-header">
        <a href={dashboardHref} className="session-detail__back" aria-label="Back to dashboard">
          <svg
            width="18"
            height="18"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            viewBox="0 0 24 24"
          >
            <path d="M19 12H5M12 19l-7-7 7-7" />
          </svg>
        </a>
        <span className={cn("session-detail__status-dot", activityToneClass(activity.color))} />
        <span className="session-detail__session-id">{session.id}</span>
        <span className={cn("session-detail__status-pill", mobileStatusPillClass(activity.label))}>
          {activity.label.toLowerCase()}
        </span>
        <span className="session-detail__time">{formatTimeCompact(session.lastActivityAt)}</span>
      </div>

      <div className={`session-detail__terminal-full${pr ? " session-detail__terminal-full--with-sheet" : ""}`}>
        {!showTerminal ? (
          <div className="session-detail-terminal-placeholder session-detail-height--full" />
        ) : terminalEnded ? (
          <div className="terminal-exited-placeholder session-detail-height--full">
            <span className="terminal-exited-placeholder__text">Terminal session has ended</span>
          </div>
        ) : (
          <DirectTerminal
            sessionId={session.id}
            startFullscreen={startFullscreen}
            variant={terminalVariant}
            appearance="dark"
            height="100%"
            chromeless
            isOpenCodeSession={isOpenCodeSession}
            reloadCommand={isOpenCodeSession ? reloadCommand : undefined}
          />
        )}
      </div>

      {pr ? (
        <div className="session-detail__bottom-sheet">
          <div className="session-detail__sheet-handle" />
          <div className="session-detail__sheet-row">
            <a
              href={pr.url}
              target="_blank"
              rel="noopener noreferrer"
              className="session-detail__sheet-pr"
            >
              PR #{pr.number}
            </a>
            <span className="session-detail__sheet-item">
              <span className={cn("session-detail__sheet-ci-dot", ciToneClass(pr))} />
              {getCiShortLabel(pr)}
            </span>
            <span className="session-detail__sheet-item">{getReviewShortLabel(pr) || "—"}</span>
          </div>
        </div>
      ) : null}

      <MobileBottomNav
        ariaLabel="Session navigation"
        activeTab={isOrchestrator ? "orchestrator" : undefined}
        dashboardHref={dashboardHref}
        prsHref={prsHref}
        showOrchestrator={orchestratorHref !== null}
        orchestratorHref={orchestratorHref}
      />
    </div>
  );
}
