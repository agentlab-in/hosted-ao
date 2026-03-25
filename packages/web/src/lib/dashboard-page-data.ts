import type { DashboardSession, DashboardOrchestratorLink } from "@/lib/types";
import { getServices, getSCM } from "@/lib/services";
import {
  sessionToDashboard,
  resolveProject,
  enrichSessionPR,
  enrichSessionsMetadata,
  listDashboardOrchestrators,
} from "@/lib/serialize";
import { prCache, prCacheKey } from "@/lib/cache";
import { getPrimaryProjectId, getProjectName, getAllProjects, type ProjectInfo } from "@/lib/project-name";
import { filterProjectSessions, filterWorkerSessions } from "@/lib/project-utils";
import { resolveGlobalPause, type GlobalPauseState } from "@/lib/global-pause";

interface DashboardPageData {
  sessions: DashboardSession[];
  globalPause: GlobalPauseState | null;
  orchestrators: DashboardOrchestratorLink[];
  projectName: string;
  projects: ProjectInfo[];
  selectedProjectId?: string;
}

function getSelectedProjectName(projectFilter: string | undefined): string {
  if (projectFilter === "all") return "All Projects";
  const projects = getAllProjects();
  if (projectFilter) {
    const selectedProject = projects.find((project) => project.id === projectFilter);
    if (selectedProject) return selectedProject.name;
  }
  return getProjectName();
}

export function resolveDashboardProjectFilter(project?: string): string {
  return project ?? getPrimaryProjectId();
}

export async function getDashboardPageData(project?: string): Promise<DashboardPageData> {
  const projectFilter = resolveDashboardProjectFilter(project);
  const pageData: DashboardPageData = {
    sessions: [],
    globalPause: null,
    orchestrators: [],
    projectName: getSelectedProjectName(projectFilter),
    projects: getAllProjects(),
    selectedProjectId: projectFilter === "all" ? undefined : projectFilter,
  };

  try {
    const { config, registry, sessionManager } = await getServices();
    const allSessions = await sessionManager.list();

    pageData.globalPause = resolveGlobalPause(allSessions);

    const visibleSessions = filterProjectSessions(allSessions, projectFilter, config.projects);
    pageData.orchestrators = listDashboardOrchestrators(visibleSessions, config.projects);

    const coreSessions = filterWorkerSessions(allSessions, projectFilter, config.projects);
    pageData.sessions = coreSessions.map(sessionToDashboard);

    const metaTimeout = new Promise<void>((resolve) => setTimeout(resolve, 3_000));
    await Promise.race([
      enrichSessionsMetadata(coreSessions, pageData.sessions, config, registry),
      metaTimeout,
    ]);

    const terminalStatuses = new Set(["merged", "killed", "cleanup", "done", "terminated"]);
    const enrichPromises = coreSessions.map((core, index) => {
      if (!core.pr) return Promise.resolve();

      const cacheKey = prCacheKey(core.pr.owner, core.pr.repo, core.pr.number);
      const cached = prCache.get(cacheKey);

      if (cached) {
        if (pageData.sessions[index].pr) {
          pageData.sessions[index].pr.state = cached.state;
          pageData.sessions[index].pr.title = cached.title;
          pageData.sessions[index].pr.additions = cached.additions;
          pageData.sessions[index].pr.deletions = cached.deletions;
          pageData.sessions[index].pr.ciStatus = cached.ciStatus as
            | "none"
            | "pending"
            | "passing"
            | "failing";
          pageData.sessions[index].pr.reviewDecision = cached.reviewDecision as
            | "none"
            | "pending"
            | "approved"
            | "changes_requested";
          pageData.sessions[index].pr.ciChecks = cached.ciChecks.map((check) => ({
            name: check.name,
            status: check.status as "pending" | "running" | "passed" | "failed" | "skipped",
            url: check.url,
          }));
          pageData.sessions[index].pr.mergeability = cached.mergeability;
          pageData.sessions[index].pr.unresolvedThreads = cached.unresolvedThreads;
          pageData.sessions[index].pr.unresolvedComments = cached.unresolvedComments;
        }

        if (
          terminalStatuses.has(core.status) ||
          cached.state === "merged" ||
          cached.state === "closed"
        ) {
          return Promise.resolve();
        }
      }

      const projectConfig = resolveProject(core, config.projects);
      const scm = getSCM(registry, projectConfig);
      if (!scm) return Promise.resolve();
      return enrichSessionPR(pageData.sessions[index], scm, core.pr);
    });
    const enrichTimeout = new Promise<void>((resolve) => setTimeout(resolve, 4_000));
    await Promise.race([Promise.allSettled(enrichPromises), enrichTimeout]);
  } catch {
    pageData.sessions = [];
    pageData.globalPause = null;
    pageData.orchestrators = [];
  }

  return pageData;
}
