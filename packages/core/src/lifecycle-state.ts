import type {
  CanonicalPRReason,
  CanonicalPRState,
  CanonicalRuntimeReason,
  CanonicalRuntimeState,
  CanonicalSessionLifecycle,
  CanonicalSessionReason,
  CanonicalSessionState,
  RuntimeHandle,
  SessionKind,
  SessionStatus,
} from "./types.js";
import { parsePrFromUrl } from "./utils/pr.js";
import { safeJsonParse, validateStatus } from "./utils/validation.js";

interface ParseCanonicalLifecycleOptions {
  sessionId?: string;
  status?: SessionStatus;
  runtimeHandle?: RuntimeHandle | null;
  createdAt?: Date;
}

function normalizeTimestamp(value: unknown, fallback: string | null = null): string | null {
  if (typeof value !== "string") return fallback;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? fallback : new Date(parsed).toISOString();
}

function normalizeRuntimeHandle(value: unknown): RuntimeHandle | null {
  if (!value || typeof value !== "object") return null;
  const record = value as Record<string, unknown>;
  if (typeof record["id"] !== "string" || typeof record["runtimeName"] !== "string") return null;
  const data = record["data"];
  return {
    id: record["id"],
    runtimeName: record["runtimeName"],
    data: data && typeof data === "object" ? (data as Record<string, unknown>) : {},
  };
}

export function createInitialCanonicalLifecycle(
  kind: SessionKind,
  now = new Date(),
): CanonicalSessionLifecycle {
  const timestamp = now.toISOString();
  return {
    version: 2,
    session: {
      kind,
      state: "not_started",
      reason: "spawn_requested",
      startedAt: null,
      completedAt: null,
      terminatedAt: null,
      lastTransitionAt: timestamp,
    },
    pr: {
      state: "none",
      reason: "not_created",
      number: null,
      url: null,
      lastObservedAt: null,
    },
    runtime: {
      state: "unknown",
      reason: "spawn_incomplete",
      lastObservedAt: null,
      handle: null,
      tmuxName: null,
    },
  };
}

function synthesizeSessionState(
  status: SessionStatus,
): { state: CanonicalSessionState; reason: CanonicalSessionReason } {
  switch (status) {
    case "spawning":
      return { state: "not_started", reason: "spawn_requested" };
    case "needs_input":
      return { state: "needs_input", reason: "awaiting_user_input" };
    case "stuck":
      return { state: "stuck", reason: "probe_failure" };
    case "errored":
      return { state: "terminated", reason: "error_in_process" };
    case "killed":
    case "terminated":
    case "cleanup":
      return { state: "terminated", reason: "manually_killed" };
    case "done":
      return { state: "done", reason: "research_complete" };
    case "merged":
      return { state: "idle", reason: "merged_waiting_decision" };
    case "idle":
      return { state: "idle", reason: "awaiting_external_review" };
    default:
      return { state: "working", reason: "task_in_progress" };
  }
}

function synthesizePRState(meta: Record<string, string>): {
  state: CanonicalPRState;
  reason: CanonicalPRReason;
  number: number | null;
  url: string | null;
} {
  const prUrl = meta["pr"] ?? null;
  if (!prUrl) {
    return { state: "none", reason: "not_created", number: null, url: null };
  }
  const parsed = parsePrFromUrl(prUrl);
  return {
    state: "open",
    reason: "in_progress",
    number: parsed?.number ?? null,
    url: prUrl,
  };
}

function synthesizeRuntimeState(
  meta: Record<string, string>,
  runtimeHandle: RuntimeHandle | null,
): { state: CanonicalRuntimeState; reason: CanonicalRuntimeReason; handle: RuntimeHandle | null; tmuxName: string | null } {
  const tmuxName = meta["tmuxName"]?.trim() || null;
  const handle =
    runtimeHandle ?? (meta["runtimeHandle"] ? safeJsonParse<RuntimeHandle>(meta["runtimeHandle"]) : null);
  if (handle || tmuxName) {
    return {
      state: "unknown",
      reason: "spawn_incomplete",
      handle: handle ?? null,
      tmuxName,
    };
  }
  return {
    state: "unknown",
    reason: "spawn_incomplete",
    handle: null,
    tmuxName: null,
  };
}

function synthesizeCanonicalLifecycle(
  meta: Record<string, string>,
  options: ParseCanonicalLifecycleOptions = {},
): CanonicalSessionLifecycle {
  const status = options.status ?? validateStatus(meta["status"]);
  const sessionKind: SessionKind =
    meta["role"] === "orchestrator" || options.sessionId?.endsWith("-orchestrator")
      ? "orchestrator"
      : "worker";
  const now =
    options.createdAt?.toISOString() ??
    normalizeTimestamp(meta["createdAt"], new Date().toISOString()) ??
    new Date().toISOString();
  const sessionState = synthesizeSessionState(status);
  const pr = synthesizePRState(meta);
  const runtime = synthesizeRuntimeState(meta, options.runtimeHandle ?? null);

  return {
    version: 2,
    session: {
      kind: sessionKind,
      state: sessionState.state,
      reason: sessionState.reason,
      startedAt: status === "spawning" ? null : now,
      completedAt: status === "done" ? now : null,
      terminatedAt:
        status === "killed" || status === "terminated" || status === "cleanup" ? now : null,
      lastTransitionAt: now,
    },
    pr: {
      state: pr.state,
      reason: pr.reason,
      number: pr.number,
      url: pr.url,
      lastObservedAt: pr.url ? now : null,
    },
    runtime: {
      state: runtime.state,
      reason: runtime.reason,
      lastObservedAt: runtime.handle || runtime.tmuxName ? now : null,
      handle: runtime.handle,
      tmuxName: runtime.tmuxName,
    },
  };
}

function normalizePayloadLifecycle(
  payload: CanonicalSessionLifecycle,
  meta: Record<string, string>,
  options: ParseCanonicalLifecycleOptions = {},
): CanonicalSessionLifecycle {
  const synthesized = synthesizeCanonicalLifecycle(meta, options);
  return {
    version: 2,
    session: {
      kind: payload.session?.kind === "orchestrator" ? "orchestrator" : synthesized.session.kind,
      state: (payload.session?.state as CanonicalSessionState | undefined) ?? synthesized.session.state,
      reason:
        (payload.session?.reason as CanonicalSessionReason | undefined) ?? synthesized.session.reason,
      startedAt: normalizeTimestamp(payload.session?.startedAt, synthesized.session.startedAt),
      completedAt: normalizeTimestamp(payload.session?.completedAt, synthesized.session.completedAt),
      terminatedAt: normalizeTimestamp(
        payload.session?.terminatedAt,
        synthesized.session.terminatedAt,
      ),
      lastTransitionAt:
        normalizeTimestamp(payload.session?.lastTransitionAt, synthesized.session.lastTransitionAt) ??
        synthesized.session.lastTransitionAt,
    },
    pr: {
      state: (payload.pr?.state as CanonicalPRState | undefined) ?? synthesized.pr.state,
      reason: (payload.pr?.reason as CanonicalPRReason | undefined) ?? synthesized.pr.reason,
      number: typeof payload.pr?.number === "number" ? payload.pr.number : synthesized.pr.number,
      url: typeof payload.pr?.url === "string" ? payload.pr.url : synthesized.pr.url,
      lastObservedAt: normalizeTimestamp(payload.pr?.lastObservedAt, synthesized.pr.lastObservedAt),
    },
    runtime: {
      state: (payload.runtime?.state as CanonicalRuntimeState | undefined) ?? synthesized.runtime.state,
      reason:
        (payload.runtime?.reason as CanonicalRuntimeReason | undefined) ??
        synthesized.runtime.reason,
      lastObservedAt: normalizeTimestamp(
        payload.runtime?.lastObservedAt,
        synthesized.runtime.lastObservedAt,
      ),
      handle: normalizeRuntimeHandle(payload.runtime?.handle) ?? synthesized.runtime.handle,
      tmuxName:
        typeof payload.runtime?.tmuxName === "string"
          ? payload.runtime.tmuxName
          : synthesized.runtime.tmuxName,
    },
  };
}

export function parseCanonicalLifecycle(
  meta: Record<string, string>,
  options: ParseCanonicalLifecycleOptions = {},
): CanonicalSessionLifecycle {
  const parsed =
    meta["statePayload"] && meta["stateVersion"] === "2"
      ? safeJsonParse<CanonicalSessionLifecycle>(meta["statePayload"])
      : null;
  if (parsed?.version === 2) {
    return normalizePayloadLifecycle(parsed, meta, options);
  }
  return synthesizeCanonicalLifecycle(meta, options);
}

export function deriveLegacyStatus(
  lifecycle: CanonicalSessionLifecycle,
  _previousStatus: SessionStatus = "working",
): SessionStatus {
  switch (lifecycle.session.state) {
    case "not_started":
      return "spawning";
    case "needs_input":
      return "needs_input";
    case "stuck":
      return "stuck";
    case "done":
      return "done";
    case "terminated":
      return "terminated";
    case "idle":
      return lifecycle.pr.state === "merged" ? "merged" : "idle";
    case "detecting":
      return "detecting";
    case "working":
      if (lifecycle.pr.reason === "ci_failing") return "ci_failed";
      if (lifecycle.pr.reason === "changes_requested") return "changes_requested";
      if (lifecycle.pr.reason === "review_pending") return "review_pending";
      if (lifecycle.pr.reason === "approved") return "approved";
      if (lifecycle.pr.reason === "merge_ready") return "mergeable";
      if (lifecycle.pr.state === "open") return "pr_open";
      return "working";
  }
}

export function buildLifecycleMetadataPatch(
  lifecycle: CanonicalSessionLifecycle,
  previousStatus?: SessionStatus,
): Partial<Record<string, string>> {
  return {
    stateVersion: "2",
    statePayload: JSON.stringify(lifecycle),
    status: deriveLegacyStatus(lifecycle, previousStatus),
    pr: lifecycle.pr.url ?? "",
    runtimeHandle: lifecycle.runtime.handle ? JSON.stringify(lifecycle.runtime.handle) : "",
    tmuxName: lifecycle.runtime.tmuxName ?? "",
    role: lifecycle.session.kind === "orchestrator" ? "orchestrator" : "",
  };
}

export function cloneLifecycle(lifecycle: CanonicalSessionLifecycle): CanonicalSessionLifecycle {
  return {
    version: 2,
    session: { ...lifecycle.session },
    pr: { ...lifecycle.pr },
    runtime: {
      ...lifecycle.runtime,
      handle: lifecycle.runtime.handle
        ? {
            id: lifecycle.runtime.handle.id,
            runtimeName: lifecycle.runtime.handle.runtimeName,
            data: { ...lifecycle.runtime.handle.data },
          }
        : null,
    },
  };
}
