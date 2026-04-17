import { describe, expect, it } from "vitest";
import { createInitialCanonicalLifecycle, deriveLegacyStatus } from "../lifecycle-state.js";

function createOpenPRLifecycle() {
  const lifecycle = createInitialCanonicalLifecycle("worker", new Date("2025-01-01T00:00:00Z"));
  lifecycle.session.startedAt = lifecycle.session.lastTransitionAt;
  lifecycle.pr.state = "open";
  lifecycle.pr.reason = "review_pending";
  lifecycle.pr.number = 42;
  lifecycle.pr.url = "https://github.com/org/repo/pull/42";
  lifecycle.pr.lastObservedAt = lifecycle.session.lastTransitionAt;
  lifecycle.runtime.state = "alive";
  lifecycle.runtime.reason = "process_running";
  return lifecycle;
}

describe("deriveLegacyStatus", () => {
  it("preserves urgent session states ahead of open PR aliases", () => {
    const needsInput = createOpenPRLifecycle();
    needsInput.session.state = "needs_input";
    needsInput.session.reason = "awaiting_user_input";

    const stuck = createOpenPRLifecycle();
    stuck.session.state = "stuck";
    stuck.session.reason = "probe_failure";

    const terminated = createOpenPRLifecycle();
    terminated.session.state = "terminated";
    terminated.session.reason = "manually_killed";

    expect(deriveLegacyStatus(needsInput)).toBe("needs_input");
    expect(deriveLegacyStatus(stuck)).toBe("stuck");
    expect(deriveLegacyStatus(terminated)).toBe("terminated");
  });

  it("preserves prior terminal legacy statuses for terminated sessions", () => {
    const terminated = createOpenPRLifecycle();
    terminated.session.state = "terminated";
    terminated.session.reason = "manually_killed";

    expect(deriveLegacyStatus(terminated, "killed")).toBe("killed");
    expect(deriveLegacyStatus(terminated, "cleanup")).toBe("cleanup");
    expect(deriveLegacyStatus(terminated, "errored")).toBe("errored");
  });

  it("keeps PR-oriented aliases for idle workers with open PRs", () => {
    const reviewPending = createOpenPRLifecycle();
    reviewPending.session.state = "idle";
    reviewPending.session.reason = "awaiting_external_review";

    const mergeReady = createOpenPRLifecycle();
    mergeReady.session.state = "idle";
    mergeReady.session.reason = "awaiting_external_review";
    mergeReady.pr.reason = "merge_ready";

    expect(deriveLegacyStatus(reviewPending)).toBe("review_pending");
    expect(deriveLegacyStatus(mergeReady)).toBe("mergeable");
  });
});
