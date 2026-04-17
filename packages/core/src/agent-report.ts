/**
 * Agent Report — explicit workflow transitions declared by the worker agent.
 *
 * Stage 3 of the state-machine redesign. Agents run `ao acknowledge` and
 * `ao report <state>` from inside a managed session to declare the workflow
 * phase they are entering. The lifecycle manager prefers fresh agent reports
 * over weak inference, but runtime evidence (process death, merged PR) and
 * SCM ground-truth (CI, review decisions) still take precedence.
 *
 * Fallback matrix (highest precedence first):
 *   1. Runtime dead + no recent activity                  → terminated/stuck
 *   2. Agent activity plugin surfaces waiting_input/exited
 *   3. SCM/PR ground truth (merged, closed, CI, reviews)
 *   4. Fresh agent report (this module)
 *   5. Idle-beyond-threshold promotion                    → stuck
 *   6. Default to working
 */

import { appendFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type {
  CanonicalSessionLifecycle,
  CanonicalSessionReason,
  CanonicalSessionState,
  SessionId,
  SessionStatus,
} from "./types.js";
import { updateCanonicalLifecycle, updateMetadata, readMetadataRaw } from "./metadata.js";
import { deriveLegacyStatus, parseCanonicalLifecycle } from "./lifecycle-state.js";
import { validateStatus } from "./utils/validation.js";

/**
 * Canonical set of states an agent can self-declare.
 *
 * - `started`           — agent has begun the task after planning
 * - `working`           — generic working signal, useful after a pause
 * - `waiting`           — blocked on an external dependency agent cannot unblock
 * - `needs_input`       — blocked on human input
 * - `fixing_ci`         — responding to a failing CI run
 * - `addressing_reviews`— responding to requested review changes
 * - `completed`         — finished research/non-coding work (not "merged")
 *
 * Note: agents cannot self-report `done`, `terminated`, or PR-state transitions.
 * Those remain owned by AO so ground-truth sources (SCM, runtime) stay
 * authoritative.
 */
export const AGENT_REPORTED_STATES = [
  "started",
  "working",
  "waiting",
  "needs_input",
  "fixing_ci",
  "addressing_reviews",
  "completed",
] as const;

export type AgentReportedState = (typeof AGENT_REPORTED_STATES)[number];

export interface AgentReport {
  state: AgentReportedState;
  /** ISO 8601 timestamp — when the agent issued the report. */
  timestamp: string;
  /** Optional free-text note the agent may include (e.g. brief status line). */
  note?: string;
  /** Local actor identity when available (e.g. $USER). */
  actor?: string;
  /** Which CLI surface produced this report. */
  source?: "acknowledge" | "report";
}

export interface AgentReportAuditSnapshot {
  legacyStatus: SessionStatus;
  sessionState: CanonicalSessionState;
  sessionReason: CanonicalSessionReason;
  lastTransitionAt: string | null;
}

export interface AgentReportAuditEntry {
  timestamp: string;
  actor: string;
  source: "acknowledge" | "report";
  reportState: AgentReportedState;
  note?: string;
  accepted: boolean;
  rejectionReason?: string;
  before: AgentReportAuditSnapshot;
  after: AgentReportAuditSnapshot;
}

/** Metadata keys written by `applyAgentReport`. Keep in sync with CLI parsing. */
export const AGENT_REPORT_METADATA_KEYS = {
  STATE: "agentReportedState",
  AT: "agentReportedAt",
  NOTE: "agentReportedNote",
} as const;

/** Freshness window — agent reports older than this are ignored. */
export const AGENT_REPORT_FRESHNESS_MS = 300_000; // 5 minutes

/**
 * CLI surface accepts these hyphen/underscore aliases for convenience.
 *
 * Note: `done` is intentionally NOT an alias — agents cannot self-report
 * terminal `done` state (AO owns that transition via SCM ground truth). Use
 * `completed` for finished non-coding research/analysis work.
 */
const INPUT_ALIASES: Record<string, AgentReportedState> = {
  "start": "started",
  "started": "started",
  "working": "working",
  "work": "working",
  "wait": "waiting",
  "waiting": "waiting",
  "needs-input": "needs_input",
  "needs_input": "needs_input",
  "input": "needs_input",
  "fixing-ci": "fixing_ci",
  "fixing_ci": "fixing_ci",
  "ci": "fixing_ci",
  "addressing-reviews": "addressing_reviews",
  "addressing_reviews": "addressing_reviews",
  "reviews": "addressing_reviews",
  "completed": "completed",
  "complete": "completed",
};

/** Normalize a user-supplied report name into the canonical form. */
export function normalizeAgentReportedState(input: string): AgentReportedState | null {
  if (!input) return null;
  return INPUT_ALIASES[input.trim().toLowerCase()] ?? null;
}

/** Canonical mapping: AgentReportedState → (canonical session state, reason). */
export function mapAgentReportToLifecycle(state: AgentReportedState): {
  sessionState: CanonicalSessionState;
  sessionReason: CanonicalSessionReason;
} {
  switch (state) {
    case "started":
      return { sessionState: "working", sessionReason: "agent_acknowledged" };
    case "working":
      return { sessionState: "working", sessionReason: "task_in_progress" };
    case "waiting":
      return { sessionState: "idle", sessionReason: "awaiting_external_review" };
    case "needs_input":
      return { sessionState: "needs_input", sessionReason: "awaiting_user_input" };
    case "fixing_ci":
      return { sessionState: "working", sessionReason: "fixing_ci" };
    case "addressing_reviews":
      return { sessionState: "working", sessionReason: "resolving_review_comments" };
    case "completed":
      return { sessionState: "idle", sessionReason: "research_complete" };
  }
}

export interface AgentReportTransitionResult {
  ok: boolean;
  reason?: string;
}

/**
 * Validate whether an agent-issued report is allowed given the current lifecycle.
 *
 * Rules:
 * - Orchestrator sessions cannot accept agent reports (orchestrator sessions
 *   are read-only coordinators).
 * - Terminal states (`done`, `terminated`) cannot be re-opened by an agent.
 * - Merged PRs cannot be re-opened by an agent (`completed`/`working` etc.
 *   attempts are rejected).
 * - Runtime state of `missing`/`exited` means the agent cannot possibly be
 *   reporting — reject so we don't silently contradict runtime truth.
 */
export function validateAgentReportTransition(
  lifecycle: CanonicalSessionLifecycle,
  _next: AgentReportedState,
): AgentReportTransitionResult {
  if (lifecycle.session.kind === "orchestrator") {
    return { ok: false, reason: "orchestrator sessions cannot self-report" };
  }
  if (lifecycle.session.state === "terminated") {
    return { ok: false, reason: "session is terminated" };
  }
  // Terminal states cannot be re-opened by an agent — including `completed`,
  // which maps back to `idle` and would otherwise reanimate a `done` session.
  if (lifecycle.session.state === "done") {
    return { ok: false, reason: "session is already done" };
  }
  if (lifecycle.pr.state === "merged") {
    return { ok: false, reason: "PR already merged" };
  }
  if (lifecycle.runtime.state === "missing" || lifecycle.runtime.state === "exited") {
    return { ok: false, reason: "runtime is not alive" };
  }
  return { ok: true };
}

export interface ApplyAgentReportInput {
  state: AgentReportedState;
  note?: string;
  actor?: string;
  source?: "acknowledge" | "report";
  /** Override the current clock — used by tests. */
  now?: Date;
}

export interface ApplyAgentReportResult {
  report: AgentReport;
  legacyStatus: SessionStatus;
  previousState: CanonicalSessionState;
  nextState: CanonicalSessionState;
  auditEntry: AgentReportAuditEntry;
}

function buildAuditDir(dataDir: string): string {
  return join(dataDir, ".agent-report-audit");
}

function buildAuditFilePath(dataDir: string, sessionId: SessionId): string {
  return join(buildAuditDir(dataDir), `${sessionId}.ndjson`);
}

function normalizeActor(actor: string | undefined): string {
  const trimmed = actor?.trim();
  if (trimmed) return trimmed;
  return "unknown";
}

function buildAuditSnapshot(
  lifecycle: CanonicalSessionLifecycle,
  legacyStatus: SessionStatus,
): AgentReportAuditSnapshot {
  return {
    legacyStatus,
    sessionState: lifecycle.session.state,
    sessionReason: lifecycle.session.reason,
    lastTransitionAt: lifecycle.session.lastTransitionAt,
  };
}

function appendAgentReportAuditEntry(
  dataDir: string,
  sessionId: SessionId,
  entry: AgentReportAuditEntry,
): void {
  const auditDir = buildAuditDir(dataDir);
  mkdirSync(auditDir, { recursive: true });
  appendFileSync(buildAuditFilePath(dataDir, sessionId), `${JSON.stringify(entry)}\n`, "utf8");
}

export function readAgentReportAuditTrail(
  dataDir: string,
  sessionId: SessionId,
): AgentReportAuditEntry[] {
  const auditFilePath = buildAuditFilePath(dataDir, sessionId);
  if (!existsSync(auditFilePath)) {
    return [];
  }

  return readFileSync(auditFilePath, "utf8")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .flatMap((line) => {
      try {
        const parsed = JSON.parse(line) as Partial<AgentReportAuditEntry>;
        if (
          typeof parsed.timestamp !== "string" ||
          typeof parsed.actor !== "string" ||
          (parsed.source !== "acknowledge" && parsed.source !== "report") ||
          !AGENT_REPORTED_STATES.includes(parsed.reportState as AgentReportedState) ||
          typeof parsed.accepted !== "boolean" ||
          !parsed.before ||
          !parsed.after
        ) {
          return [];
        }
        return [parsed as AgentReportAuditEntry];
      } catch {
        return [];
      }
    })
    .reverse();
}

/**
 * Apply an agent report to a session: update the canonical lifecycle on disk
 * and persist the report metadata keys. Throws when the transition is rejected.
 *
 * The write is idempotent: applying the same report twice is safe (lifecycle
 * fields are already set, metadata timestamp refreshes).
 */
export function applyAgentReport(
  dataDir: string,
  sessionId: SessionId,
  input: ApplyAgentReportInput,
): ApplyAgentReportResult {
  const raw = readMetadataRaw(dataDir, sessionId);
  if (!raw) {
    throw new Error(`Session not found: ${sessionId}`);
  }

  const now = (input.now ?? new Date()).toISOString();
  const source = input.source ?? "report";
  const actor = normalizeActor(input.actor);
  const trimmedNote = input.note?.trim() || undefined;
  const currentLifecycle = parseCanonicalLifecycle(raw, {
    sessionId,
    status: validateStatus(raw["status"]),
  });
  const previousLegacyStatus = validateStatus(raw["status"]);
  const before = buildAuditSnapshot(currentLifecycle, previousLegacyStatus);
  const validation = validateAgentReportTransition(currentLifecycle, input.state);
  if (!validation.ok) {
    appendAgentReportAuditEntry(dataDir, sessionId, {
      timestamp: now,
      actor,
      source,
      reportState: input.state,
      note: trimmedNote,
      accepted: false,
      rejectionReason: validation.reason ?? "transition rejected",
      before,
      after: before,
    });
    throw new Error(validation.reason ?? "transition rejected");
  }

  let previousState: CanonicalSessionState | null = null;
  let nextState: CanonicalSessionState | null = null;
  let legacyStatus: SessionStatus | null = null;

  const nextLifecycle = updateCanonicalLifecycle(
    dataDir,
    sessionId,
    (current) => {
      const mapped = mapAgentReportToLifecycle(input.state);
      previousState = current.session.state;
      nextState = mapped.sessionState;
      current.session.state = mapped.sessionState;
      current.session.reason = mapped.sessionReason;
      current.session.lastTransitionAt = now;
      if (mapped.sessionState === "working" && current.session.startedAt === null) {
        current.session.startedAt = now;
      }
      legacyStatus = deriveLegacyStatus(current, previousLegacyStatus);
      return current;
    },
  );

  if (!nextLifecycle || !previousState || !nextState || !legacyStatus) {
    throw new Error(`Failed to apply agent report for session ${sessionId}`);
  }

  // Persist report metadata alongside the lifecycle patch.
  const metadataUpdates: Record<string, string> = {
    [AGENT_REPORT_METADATA_KEYS.STATE]: input.state,
    [AGENT_REPORT_METADATA_KEYS.AT]: now,
  };
  if (trimmedNote) {
    metadataUpdates[AGENT_REPORT_METADATA_KEYS.NOTE] = trimmedNote;
  } else {
    // Clear stale notes from previous reports so they don't mislead humans.
    metadataUpdates[AGENT_REPORT_METADATA_KEYS.NOTE] = "";
  }
  updateMetadata(dataDir, sessionId, metadataUpdates);

  const after = buildAuditSnapshot(nextLifecycle, legacyStatus);
  const auditEntry: AgentReportAuditEntry = {
    timestamp: now,
    actor,
    source,
    reportState: input.state,
    note: trimmedNote,
    accepted: true,
    before,
    after,
  };
  appendAgentReportAuditEntry(dataDir, sessionId, auditEntry);

  return {
    report: {
      state: input.state,
      timestamp: now,
      note: trimmedNote,
      actor,
      source,
    },
    legacyStatus,
    previousState,
    nextState,
    auditEntry,
  };
}

/** Read an agent report out of a session's raw metadata, or null if absent. */
export function readAgentReport(meta: Record<string, string> | null | undefined): AgentReport | null {
  if (!meta) return null;
  const state = meta[AGENT_REPORT_METADATA_KEYS.STATE];
  const at = meta[AGENT_REPORT_METADATA_KEYS.AT];
  if (!state || !at) return null;
  if (!AGENT_REPORTED_STATES.includes(state as AgentReportedState)) return null;
  const parsed = Date.parse(at);
  if (Number.isNaN(parsed)) return null;
  const note = meta[AGENT_REPORT_METADATA_KEYS.NOTE];
  return {
    state: state as AgentReportedState,
    timestamp: new Date(parsed).toISOString(),
    note: note && note.length > 0 ? note : undefined,
  };
}

/**
 * Check whether an agent report is fresh (within the freshness window).
 *
 * Future timestamps (clock skew, malformed input) are rejected — otherwise a
 * single skewed `agentReportedAt` could appear "fresh" indefinitely and
 * override stronger inference signals.
 */
export function isAgentReportFresh(
  report: AgentReport,
  now: Date = new Date(),
  windowMs: number = AGENT_REPORT_FRESHNESS_MS,
): boolean {
  const reportedAt = Date.parse(report.timestamp);
  if (Number.isNaN(reportedAt)) return false;
  const currentTime = now.getTime();
  if (reportedAt > currentTime) return false;
  return currentTime - reportedAt <= windowMs;
}
