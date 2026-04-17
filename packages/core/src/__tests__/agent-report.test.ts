import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { randomUUID } from "node:crypto";
import {
  AGENT_REPORTED_STATES,
  AGENT_REPORT_FRESHNESS_MS,
  AGENT_REPORT_METADATA_KEYS,
  applyAgentReport,
  isAgentReportFresh,
  mapAgentReportToLifecycle,
  normalizeAgentReportedState,
  readAgentReport,
  validateAgentReportTransition,
} from "../agent-report.js";
import { writeMetadata, writeCanonicalLifecycle, readMetadataRaw } from "../metadata.js";
import { createInitialCanonicalLifecycle } from "../lifecycle-state.js";
import type { CanonicalSessionLifecycle } from "../types.js";

let dataDir: string;

beforeEach(() => {
  dataDir = join(tmpdir(), `ao-test-agent-report-${randomUUID()}`);
  mkdirSync(dataDir, { recursive: true });
});

afterEach(() => {
  rmSync(dataDir, { recursive: true, force: true });
});

function seedWorkerSession(
  sessionId: string,
  init?: Partial<CanonicalSessionLifecycle["session"]>,
): CanonicalSessionLifecycle {
  const lifecycle = createInitialCanonicalLifecycle("worker");
  // Default the seeded lifecycle to "working" with an existing startedAt so
  // tests exercise the transition-applied path (not the first-start path).
  lifecycle.session.state = "working";
  lifecycle.session.reason = "task_in_progress";
  lifecycle.session.startedAt = "2024-12-01T00:00:00.000Z";
  if (init) {
    Object.assign(lifecycle.session, init);
  }
  lifecycle.runtime.state = "alive";
  lifecycle.runtime.reason = "process_running";
  writeMetadata(dataDir, sessionId, {
    worktree: "/tmp/worktree",
    branch: "feat/x",
    status: "working",
    project: "demo",
  });
  writeCanonicalLifecycle(dataDir, sessionId, lifecycle);
  return lifecycle;
}

describe("normalizeAgentReportedState", () => {
  it("accepts canonical values", () => {
    for (const state of AGENT_REPORTED_STATES) {
      expect(normalizeAgentReportedState(state)).toBe(state);
    }
  });

  it("accepts hyphen and short aliases", () => {
    expect(normalizeAgentReportedState("needs-input")).toBe("needs_input");
    expect(normalizeAgentReportedState("fixing-ci")).toBe("fixing_ci");
    expect(normalizeAgentReportedState("addressing-reviews")).toBe("addressing_reviews");
    expect(normalizeAgentReportedState("ci")).toBe("fixing_ci");
    expect(normalizeAgentReportedState("reviews")).toBe("addressing_reviews");
    expect(normalizeAgentReportedState("complete")).toBe("completed");
    expect(normalizeAgentReportedState("input")).toBe("needs_input");
    expect(normalizeAgentReportedState("start")).toBe("started");
    expect(normalizeAgentReportedState("work")).toBe("working");
    expect(normalizeAgentReportedState("wait")).toBe("waiting");
  });

  it("does not alias `done` (agents cannot self-report terminal done)", () => {
    expect(normalizeAgentReportedState("done")).toBeNull();
  });

  it("is case-insensitive and trims whitespace", () => {
    expect(normalizeAgentReportedState(" WAITING ")).toBe("waiting");
    expect(normalizeAgentReportedState("Needs-Input")).toBe("needs_input");
  });

  it("returns null for unknown values", () => {
    expect(normalizeAgentReportedState("foo")).toBeNull();
    expect(normalizeAgentReportedState("")).toBeNull();
  });
});

describe("mapAgentReportToLifecycle", () => {
  it("maps every reportable state to a canonical pair", () => {
    for (const state of AGENT_REPORTED_STATES) {
      const mapped = mapAgentReportToLifecycle(state);
      expect(mapped.sessionState).toBeTypeOf("string");
      expect(mapped.sessionReason).toBeTypeOf("string");
    }
  });

  it("maps needs_input to the canonical needs_input state", () => {
    expect(mapAgentReportToLifecycle("needs_input")).toEqual({
      sessionState: "needs_input",
      sessionReason: "awaiting_user_input",
    });
  });

  it("maps waiting and completed to idle (non-terminal)", () => {
    expect(mapAgentReportToLifecycle("waiting").sessionState).toBe("idle");
    expect(mapAgentReportToLifecycle("completed").sessionState).toBe("idle");
  });

  it("maps fixing_ci and addressing_reviews to working with the right reason", () => {
    expect(mapAgentReportToLifecycle("fixing_ci")).toEqual({
      sessionState: "working",
      sessionReason: "fixing_ci",
    });
    expect(mapAgentReportToLifecycle("addressing_reviews")).toEqual({
      sessionState: "working",
      sessionReason: "resolving_review_comments",
    });
  });
});

describe("validateAgentReportTransition", () => {
  it("rejects orchestrator sessions", () => {
    const lifecycle = createInitialCanonicalLifecycle("orchestrator");
    const result = validateAgentReportTransition(lifecycle, "working");
    expect(result.ok).toBe(false);
    expect(result.reason).toMatch(/orchestrator/);
  });

  it("rejects terminated sessions", () => {
    const lifecycle = createInitialCanonicalLifecycle("worker");
    lifecycle.session.state = "terminated";
    const result = validateAgentReportTransition(lifecycle, "working");
    expect(result.ok).toBe(false);
  });

  it("rejects all reports when session is done (terminal state cannot reopen)", () => {
    const lifecycle = createInitialCanonicalLifecycle("worker");
    lifecycle.session.state = "done";
    // `completed` maps back to `idle` and would reanimate a `done` session, so
    // it must also be rejected — not just the obvious working/needs_input ones.
    expect(validateAgentReportTransition(lifecycle, "working").ok).toBe(false);
    expect(validateAgentReportTransition(lifecycle, "completed").ok).toBe(false);
    expect(validateAgentReportTransition(lifecycle, "needs_input").ok).toBe(false);
  });

  it("rejects reports on merged PRs", () => {
    const lifecycle = createInitialCanonicalLifecycle("worker");
    lifecycle.pr.state = "merged";
    expect(validateAgentReportTransition(lifecycle, "working").ok).toBe(false);
  });

  it("rejects reports when runtime is missing or exited", () => {
    const missing = createInitialCanonicalLifecycle("worker");
    missing.runtime.state = "missing";
    expect(validateAgentReportTransition(missing, "working").ok).toBe(false);

    const exited = createInitialCanonicalLifecycle("worker");
    exited.runtime.state = "exited";
    expect(validateAgentReportTransition(exited, "working").ok).toBe(false);
  });

  it("accepts valid worker transitions", () => {
    const lifecycle = createInitialCanonicalLifecycle("worker");
    lifecycle.session.state = "working";
    lifecycle.runtime.state = "alive";
    expect(validateAgentReportTransition(lifecycle, "fixing_ci").ok).toBe(true);
    expect(validateAgentReportTransition(lifecycle, "needs_input").ok).toBe(true);
  });
});

describe("applyAgentReport", () => {
  const sessionId = "demo-1";

  beforeEach(() => {
    seedWorkerSession(sessionId);
  });

  it("writes canonical lifecycle and metadata keys", () => {
    const now = new Date("2025-01-01T12:00:00.000Z");
    const result = applyAgentReport(dataDir, sessionId, {
      state: "needs_input",
      note: " please clarify the spec ",
      now,
    });

    expect(result.report.state).toBe("needs_input");
    expect(result.report.timestamp).toBe(now.toISOString());
    expect(result.report.note).toBe("please clarify the spec");
    expect(result.nextState).toBe("needs_input");

    const meta = readMetadataRaw(dataDir, sessionId);
    expect(meta).not.toBeNull();
    expect(meta![AGENT_REPORT_METADATA_KEYS.STATE]).toBe("needs_input");
    expect(meta![AGENT_REPORT_METADATA_KEYS.AT]).toBe(now.toISOString());
    expect(meta![AGENT_REPORT_METADATA_KEYS.NOTE]).toBe("please clarify the spec");
  });

  it("sets startedAt on the first working transition", () => {
    const now = new Date("2025-01-01T12:00:00.000Z");
    // Re-seed with startedAt explicitly null so we exercise the first-start
    // branch of applyAgentReport.
    seedWorkerSession(sessionId, {
      state: "not_started",
      reason: "spawn_requested",
      startedAt: null,
    });
    applyAgentReport(dataDir, sessionId, { state: "started", now });
    const meta = readMetadataRaw(dataDir, sessionId);
    expect(meta).not.toBeNull();
    // The canonical payload is stored in statePayload as JSON.
    const payload = JSON.parse(meta!["statePayload"]);
    expect(payload.session.state).toBe("working");
    expect(payload.session.reason).toBe("agent_acknowledged");
    expect(payload.session.startedAt).toBe(now.toISOString());
  });

  it("clears a previous note when none is supplied", () => {
    applyAgentReport(dataDir, sessionId, {
      state: "working",
      note: "first note",
      now: new Date("2025-01-01T11:00:00.000Z"),
    });
    applyAgentReport(dataDir, sessionId, {
      state: "working",
      now: new Date("2025-01-01T12:00:00.000Z"),
    });
    const meta = readMetadataRaw(dataDir, sessionId);
    expect(meta).not.toBeNull();
    expect(meta![AGENT_REPORT_METADATA_KEYS.NOTE] ?? "").toBe("");
  });

  it("throws when the transition is rejected", () => {
    // Force lifecycle into a terminated state and try to re-report.
    const lifecycle = createInitialCanonicalLifecycle("worker");
    lifecycle.session.state = "terminated";
    writeCanonicalLifecycle(dataDir, sessionId, lifecycle);
    expect(() =>
      applyAgentReport(dataDir, sessionId, {
        state: "working",
        now: new Date(),
      }),
    ).toThrow(/terminated/);
  });

  it("throws when the session does not exist", () => {
    expect(() =>
      applyAgentReport(dataDir, "missing-session", {
        state: "working",
        now: new Date(),
      }),
    ).toThrow(/not found/);
  });
});

describe("readAgentReport + isAgentReportFresh", () => {
  it("returns null when metadata lacks report keys", () => {
    expect(readAgentReport({})).toBeNull();
    expect(readAgentReport(null)).toBeNull();
    expect(readAgentReport(undefined)).toBeNull();
  });

  it("returns null for unknown states or bad timestamps", () => {
    expect(
      readAgentReport({
        [AGENT_REPORT_METADATA_KEYS.STATE]: "not-a-state",
        [AGENT_REPORT_METADATA_KEYS.AT]: new Date().toISOString(),
      }),
    ).toBeNull();
    expect(
      readAgentReport({
        [AGENT_REPORT_METADATA_KEYS.STATE]: "working",
        [AGENT_REPORT_METADATA_KEYS.AT]: "not-a-timestamp",
      }),
    ).toBeNull();
  });

  it("parses a valid report with note", () => {
    const at = "2025-01-01T00:00:00.000Z";
    const report = readAgentReport({
      [AGENT_REPORT_METADATA_KEYS.STATE]: "fixing_ci",
      [AGENT_REPORT_METADATA_KEYS.AT]: at,
      [AGENT_REPORT_METADATA_KEYS.NOTE]: "still debugging",
    });
    expect(report).toEqual({ state: "fixing_ci", timestamp: at, note: "still debugging" });
  });

  it("treats an empty note as absent", () => {
    const at = "2025-01-01T00:00:00.000Z";
    const report = readAgentReport({
      [AGENT_REPORT_METADATA_KEYS.STATE]: "working",
      [AGENT_REPORT_METADATA_KEYS.AT]: at,
      [AGENT_REPORT_METADATA_KEYS.NOTE]: "",
    });
    expect(report?.note).toBeUndefined();
  });

  it("reports freshness against the default window", () => {
    const now = new Date("2025-01-01T12:05:00.000Z");
    const freshAt = "2025-01-01T12:04:00.000Z"; // 1m old
    const staleAt = "2025-01-01T11:55:00.000Z"; // 10m old
    const fresh = readAgentReport({
      [AGENT_REPORT_METADATA_KEYS.STATE]: "working",
      [AGENT_REPORT_METADATA_KEYS.AT]: freshAt,
    })!;
    const stale = readAgentReport({
      [AGENT_REPORT_METADATA_KEYS.STATE]: "working",
      [AGENT_REPORT_METADATA_KEYS.AT]: staleAt,
    })!;
    expect(isAgentReportFresh(fresh, now)).toBe(true);
    expect(isAgentReportFresh(stale, now)).toBe(false);
  });

  it("rejects future timestamps (clock skew must not appear forever-fresh)", () => {
    const now = new Date("2025-01-01T12:00:00.000Z");
    const futureAt = "2025-01-01T12:10:00.000Z"; // 10m in the future
    const future = readAgentReport({
      [AGENT_REPORT_METADATA_KEYS.STATE]: "working",
      [AGENT_REPORT_METADATA_KEYS.AT]: futureAt,
    })!;
    expect(isAgentReportFresh(future, now)).toBe(false);
  });

  it("exposes the default freshness window (5 minutes)", () => {
    expect(AGENT_REPORT_FRESHNESS_MS).toBe(5 * 60 * 1000);
  });
});
