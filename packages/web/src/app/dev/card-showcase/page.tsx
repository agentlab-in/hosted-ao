"use client";

import { AttentionZone } from "@/components/AttentionZone";
import { SessionCard } from "@/components/SessionCard";
import { ThemeToggle } from "@/components/ThemeToggle";
import {
  getAttentionLevel,
  type AttentionLevel,
  type DashboardPR,
  type DashboardSession,
} from "@/lib/types";

export const dynamic = "force-dynamic";

function makePR(overrides: Partial<DashboardPR> = {}): DashboardPR {
  return {
    number: 125,
    url: "https://github.com/composio/agent-orchestrator/pull/125",
    title: "feat(web): redesign dashboard, session detail, and orchestrator terminal",
    owner: "composio",
    repo: "agent-orchestrator",
    branch: "feat/dashboard-redesign",
    baseBranch: "main",
    isDraft: false,
    state: "open",
    additions: 3730,
    deletions: 697,
    ciStatus: "passing",
    ciChecks: [
      { name: "build", status: "passed", url: "https://github.com/composio/agent-orchestrator/actions" },
      { name: "typecheck", status: "passed", url: "https://github.com/composio/agent-orchestrator/actions" },
    ],
    reviewDecision: "approved",
    mergeability: {
      mergeable: true,
      ciPassing: true,
      approved: true,
      noConflicts: true,
      blockers: [],
    },
    unresolvedThreads: 0,
    unresolvedComments: [],
    ...overrides,
  };
}

function makeSession(overrides: Partial<DashboardSession> = {}): DashboardSession {
  return {
    id: "ao-58",
    projectId: "agent-orchestrator",
    status: "working",
    activity: "idle",
    branch: "feat/dashboard-redesign",
    issueId: "https://linear.app/composio/issue/AO-557",
    issueUrl: "https://linear.app/composio/issue/AO-557",
    issueLabel: "#557",
    issueTitle: "Dashboard UI: Phase 8 - Multi-Project Sidebar Redesign",
    summary: "Dashboard UI: Phase 8 - Multi-Project Sidebar Redesign",
    summaryIsFallback: false,
    createdAt: new Date().toISOString(),
    lastActivityAt: new Date().toISOString(),
    pr: null,
    metadata: {},
    ...overrides,
  };
}

const noop = () => {};

const showcaseCards: Array<{ title: string; note: string; session: DashboardSession }> = [
  {
    title: "Working",
    note: "Default in-flight work with no PR pressure.",
    session: makeSession({
      id: "ao-41",
      activity: "active",
      status: "working",
      branch: "feat/parallel-agents",
      issueLabel: "#541",
      issueTitle: "Improve worker session orchestration",
      summary: "Ship runtime orchestration improvements across worker sessions",
    }),
  },
  {
    title: "Respond",
    note: "Human input required with all ask-to action tags present.",
    session: makeSession({
      id: "ao-58",
      status: "needs_input",
      activity: "waiting_input",
      branch: "session/ao-58",
      pr: makePR({
        ciStatus: "failing",
        ciChecks: [
          { name: "CI", status: "failed", url: "https://github.com/composio/agent-orchestrator/actions/runs/1" },
        ],
        reviewDecision: "pending",
        mergeability: {
          mergeable: false,
          ciPassing: false,
          approved: false,
          noConflicts: true,
          blockers: ["CI failing", "Review required"],
        },
        unresolvedThreads: 3,
        unresolvedComments: [
          {
            url: "https://github.com/composio/agent-orchestrator/pull/125#discussion_r1",
            path: "packages/web/src/components/Dashboard.tsx",
            author: "reviewer",
            body: "Resolve before landing",
          },
          {
            url: "https://github.com/composio/agent-orchestrator/pull/125#discussion_r2",
            path: "packages/web/src/components/SessionCard.tsx",
            author: "reviewer",
            body: "Tighten state handling",
          },
          {
            url: "https://github.com/composio/agent-orchestrator/pull/125#discussion_r3",
            path: "packages/web/src/app/globals.css",
            author: "reviewer",
            body: "Spacing polish",
          },
        ],
      }),
    }),
  },
  {
    title: "Review",
    note: "Changes requested state with review pressure.",
    session: makeSession({
      id: "ao-63",
      status: "changes_requested",
      activity: "blocked",
      branch: "feat/pr-review-flow",
      issueLabel: "#563",
      issueTitle: "Improve review routing for agent sessions",
      summary: "Address reviewer feedback and route sessions back into the queue",
      pr: makePR({
        number: 131,
        url: "https://github.com/composio/agent-orchestrator/pull/131",
        additions: 842,
        deletions: 119,
        ciStatus: "passing",
        reviewDecision: "changes_requested",
        mergeability: {
          mergeable: false,
          ciPassing: true,
          approved: false,
          noConflicts: true,
          blockers: ["Changes requested"],
        },
      }),
    }),
  },
  {
    title: "Pending",
    note: "Nothing to do yet; waiting on CI and reviewers.",
    session: makeSession({
      id: "ao-77",
      status: "review_pending",
      activity: "idle",
      branch: "feat/slack-notifier",
      issueLabel: "#577",
      issueTitle: "Add Slack notification escalation",
      pr: makePR({
        number: 141,
        url: "https://github.com/composio/agent-orchestrator/pull/141",
        additions: 201,
        deletions: 48,
        ciStatus: "pending",
        reviewDecision: "pending",
        mergeability: {
          mergeable: false,
          ciPassing: false,
          approved: false,
          noConflicts: true,
          blockers: ["CI pending", "Review pending"],
        },
      }),
    }),
  },
  {
    title: "Merge Ready",
    note: "Clean PR with green CI and approval.",
    session: makeSession({
      id: "ao-88",
      status: "approved",
      activity: "ready",
      branch: "feat/merge-automation",
      issueLabel: "#588",
      issueTitle: "Automate merge-ready session handling",
      pr: makePR({
        number: 155,
        url: "https://github.com/composio/agent-orchestrator/pull/155",
        additions: 129,
        deletions: 22,
      }),
    }),
  },
  {
    title: "Rate Limited",
    note: "PR data degraded by GitHub API rate limiting.",
    session: makeSession({
      id: "ao-92",
      status: "working",
      activity: "idle",
      branch: "feat/github-cache",
      issueLabel: "#592",
      issueTitle: "Add PR cache fallback for rate limits",
      pr: makePR({
        number: 161,
        url: "https://github.com/composio/agent-orchestrator/pull/161",
        ciStatus: "failing",
        reviewDecision: "pending",
        mergeability: {
          mergeable: false,
          ciPassing: false,
          approved: false,
          noConflicts: false,
          blockers: ["API rate limited or unavailable"],
        },
      }),
    }),
  },
  {
    title: "Done",
    note: "Completed / merged card variant.",
    session: makeSession({
      id: "ao-99",
      status: "merged",
      activity: "exited",
      branch: "feat/card-showcase",
      issueLabel: "#599",
      issueTitle: "Create card showcase page",
      pr: makePR({
        number: 170,
        url: "https://github.com/composio/agent-orchestrator/pull/170",
        state: "merged",
      }),
    }),
  },
];

export default function CardShowcasePage() {
  const boardSessions = showcaseCards.map((entry) => entry.session);
  const grouped: Record<AttentionLevel, DashboardSession[]> = {
    merge: [],
    respond: [],
    review: [],
    pending: [],
    working: [],
    done: [],
  };

  for (const session of boardSessions) {
    grouped[getAttentionLevel(session)].push(session);
  }

  const stats = {
    total: boardSessions.length,
    active: boardSessions.filter((session) => session.activity && session.activity !== "exited")
      .length,
    prs: boardSessions.filter((session) => session.pr?.state === "open").length,
    review: grouped.review.length,
  };

  return (
    <div className="min-h-screen bg-[var(--color-bg-base)] px-6 py-8 text-[var(--color-text-primary)] md:px-10">
      <div className="mx-auto max-w-[1500px]">
        <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--color-text-secondary)]">
          Dev Showcase
        </div>

        <section className="dashboard-hero mb-8">
          <div className="dashboard-hero__backdrop" />
          <div className="dashboard-hero__content">
            <div className="dashboard-hero__primary">
              <div className="dashboard-hero__heading">
                <div>
                  <h1 className="dashboard-title">agent-orchestrator</h1>
                  <p className="dashboard-subtitle">
                    Static dashboard replica with mocked lane states and card variants for design
                    review.
                  </p>
                </div>
              </div>
              <div className="dashboard-stat-cards">
                <div className="dashboard-stat-card">
                  <span className="dashboard-stat-card__value">{stats.total}</span>
                  <span className="dashboard-stat-card__label">Fleet</span>
                </div>
                <div className="dashboard-stat-card">
                  <span
                    className="dashboard-stat-card__value"
                    style={{ color: "var(--color-status-working)" }}
                  >
                    {stats.active}
                  </span>
                  <span className="dashboard-stat-card__label">Active</span>
                </div>
                <div className="dashboard-stat-card">
                  <span className="dashboard-stat-card__value">{stats.prs}</span>
                  <span className="dashboard-stat-card__label">PRs</span>
                </div>
                <div className="dashboard-stat-card">
                  <span
                    className="dashboard-stat-card__value"
                    style={{ color: "var(--color-status-attention)" }}
                  >
                    {stats.review}
                  </span>
                  <span className="dashboard-stat-card__label">Review</span>
                </div>
              </div>
            </div>

            <div className="dashboard-hero__meta">
              <div className="flex items-center gap-3">
                <a href="#" className="orchestrator-btn flex items-center gap-2 px-4 py-2 text-[12px] font-semibold hover:no-underline">
                  <span className="h-1.5 w-1.5 bg-[var(--color-accent)] opacity-80" />
                  orchestrator
                  <svg
                    className="h-3 w-3 opacity-70"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    viewBox="0 0 24 24"
                  >
                    <path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6M15 3h6v6M10 14L21 3" />
                  </svg>
                </a>
                <ThemeToggle />
              </div>
            </div>
          </div>
        </section>

        <section className="mb-10">
          <div className="board-section-head">
            <div>
              <h2 className="board-section-head__title">Attention Board</h2>
              <p className="board-section-head__subtitle">
                Full mocked dashboard board for visual review.
              </p>
            </div>
            <div className="board-section-head__legend">
              <span className="board-legend-item">
                <span
                  className="board-legend-item__dot"
                  style={{ background: "var(--color-status-error)" }}
                />
                Human action
              </span>
              <span className="board-legend-item">
                <span
                  className="board-legend-item__dot"
                  style={{ background: "var(--color-accent-orange)" }}
                />
                Review queue
              </span>
              <span className="board-legend-item">
                <span
                  className="board-legend-item__dot"
                  style={{ background: "var(--color-status-ready)" }}
                />
                Ready to land
              </span>
            </div>
          </div>

          <div className="kanban-board-wrap">
            <div className="kanban-board">
              <AttentionZone level="working" sessions={grouped.working} onSend={noop} onKill={noop} onMerge={noop} onRestore={noop} />
              <AttentionZone level="pending" sessions={grouped.pending} onSend={noop} onKill={noop} onMerge={noop} onRestore={noop} />
              <AttentionZone level="review" sessions={grouped.review} onSend={noop} onKill={noop} onMerge={noop} onRestore={noop} />
              <AttentionZone level="respond" sessions={grouped.respond} onSend={noop} onKill={noop} onMerge={noop} onRestore={noop} />
              <AttentionZone level="merge" sessions={grouped.merge} onSend={noop} onKill={noop} onMerge={noop} onRestore={noop} />
            </div>
          </div>
        </section>

        <section className="mb-10">
          <div className="mb-4 text-[12px] font-semibold uppercase tracking-[0.16em] text-[var(--color-text-secondary)]">
            Card Library
          </div>
          <div className="grid gap-6 md:grid-cols-2 xl:grid-cols-3">
            {showcaseCards.map(({ title, note, session }) => (
              <div
                key={session.id}
                className="border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-4"
              >
                <div className="mb-3">
                  <h2 className="text-[18px] font-semibold tracking-[-0.03em]">{title}</h2>
                  <p className="mt-1 text-[12px] leading-5 text-[var(--color-text-secondary)]">
                    {note}
                  </p>
                </div>
                <SessionCard
                  session={session}
                  onSend={(_sessionId, message) => {
                    console.log("showcase send", message);
                  }}
                  onKill={noop}
                  onMerge={noop}
                  onRestore={noop}
                />
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
