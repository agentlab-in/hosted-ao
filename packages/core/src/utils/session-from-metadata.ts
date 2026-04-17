import type { ActivitySignal, RuntimeHandle, Session, SessionId, SessionStatus } from "../types.js";
import { deriveLegacyStatus, parseCanonicalLifecycle } from "../lifecycle-state.js";
import { createActivitySignal } from "../activity-signal.js";
import { parsePrFromUrl } from "./pr.js";
import { safeJsonParse, validateStatus } from "./validation.js";

interface SessionFromMetadataOptions {
  projectId?: string;
  status?: SessionStatus;
  activity?: Session["activity"];
  activitySignal?: ActivitySignal;
  runtimeHandle?: RuntimeHandle | null;
  createdAt?: Date;
  lastActivityAt?: Date;
  restoredAt?: Date;
}

export function sessionFromMetadata(
  sessionId: SessionId,
  meta: Record<string, string>,
  options: SessionFromMetadataOptions = {},
): Session {
  const runtimeHandle =
    options.runtimeHandle !== undefined
      ? options.runtimeHandle
      : meta["runtimeHandle"]
        ? safeJsonParse<RuntimeHandle>(meta["runtimeHandle"])
        : null;
  const lifecycle = parseCanonicalLifecycle(meta, {
    sessionId,
    status: options.status ?? validateStatus(meta["status"]),
    runtimeHandle,
    createdAt: options.createdAt,
  });
  const status = options.status ?? deriveLegacyStatus(lifecycle, validateStatus(meta["status"]));
  const prUrl = lifecycle.pr.url ?? meta["pr"];

  return {
    id: sessionId,
    projectId: meta["project"] ?? options.projectId ?? "",
    status,
    activity: options.activity ?? null,
    activitySignal: options.activitySignal ?? createActivitySignal("unavailable"),
    lifecycle,
    branch: meta["branch"] || null,
    issueId: meta["issue"] || null,
    pr: prUrl
      ? (() => {
          const parsed = parsePrFromUrl(prUrl);
          return {
            number: lifecycle.pr.number ?? parsed?.number ?? 0,
            url: prUrl,
            title: "",
            owner: parsed?.owner ?? "",
            repo: parsed?.repo ?? "",
            branch: meta["branch"] ?? "",
            baseBranch: "",
            isDraft: false,
          };
        })()
      : null,
    workspacePath: meta["worktree"] || null,
    runtimeHandle: lifecycle.runtime.handle ?? runtimeHandle,
    agentInfo: meta["summary"] ? { summary: meta["summary"], agentSessionId: null } : null,
    createdAt: meta["createdAt"] ? new Date(meta["createdAt"]) : (options.createdAt ?? new Date()),
    lastActivityAt: options.lastActivityAt ?? new Date(),
    restoredAt:
      options.restoredAt ?? (meta["restoredAt"] ? new Date(meta["restoredAt"]) : undefined),
    metadata: meta,
  };
}
