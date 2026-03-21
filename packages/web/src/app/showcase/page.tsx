"use client";

import { useState } from "react";
import type { DashboardSession, DashboardPR, AttentionLevel, DashboardCICheck } from "@/lib/types";
import { AttentionZone } from "@/components/AttentionZone";
import { SessionCard } from "@/components/SessionCard";
import { ActivityDot } from "@/components/ActivityDot";
import { CIBadge } from "@/components/CIBadge";
import { ThemeToggle } from "@/components/ThemeToggle";
import { ProjectSidebar } from "@/components/ProjectSidebar";
import type { ProjectInfo } from "@/lib/project-name";

// ── Mock Data ────────────────────────────────────────────────────────────

function makePR(overrides: Partial<DashboardPR> = {}): DashboardPR {
  return {
    number: 142,
    url: "#",
    title: "Add user authentication flow with OAuth2",
    owner: "acme",
    repo: "agent-orchestrator",
    branch: "feat/auth-flow",
    baseBranch: "main",
    isDraft: false,
    state: "open",
    additions: 234,
    deletions: 48,
    ciStatus: "passing",
    ciChecks: [
      { name: "build", status: "passed" },
      { name: "lint", status: "passed" },
      { name: "test", status: "passed" },
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
    id: `ses_${Math.random().toString(36).slice(2, 8)}`,
    projectId: "proj_alpha",
    status: "working",
    activity: "active",
    branch: "feat/add-feature",
    issueId: null,
    issueUrl: null,
    issueLabel: null,
    issueTitle: null,
    summary: null,
    summaryIsFallback: false,
    createdAt: new Date().toISOString(),
    lastActivityAt: new Date().toISOString(),
    pr: null,
    metadata: {},
    ...overrides,
  };
}

// ── Merge Ready sessions
const mergeSessions: DashboardSession[] = [
  makeSession({
    id: "ses_m1",
    status: "approved",
    activity: "ready",
    branch: "feat/auth-flow",
    pr: makePR({
      number: 142,
      title: "Add user authentication flow with OAuth2",
      additions: 234,
      deletions: 48,
    }),
  }),
  makeSession({
    id: "ses_m2",
    status: "approved",
    activity: "ready",
    branch: "fix/rate-limiter",
    pr: makePR({
      number: 156,
      title: "Fix rate limiter edge case on burst traffic",
      additions: 18,
      deletions: 5,
    }),
  }),
];

// ── Respond sessions
const respondSessions: DashboardSession[] = [
  makeSession({
    id: "ses_r1",
    status: "needs_input",
    activity: "waiting_input",
    branch: "feat/billing-integration",
    summary: "Waiting for Stripe API keys to proceed with billing integration",
    pr: makePR({
      number: 160,
      title: "Integrate Stripe billing with subscription tiers",
      ciStatus: "passing",
      reviewDecision: "pending",
      mergeability: {
        mergeable: false,
        ciPassing: true,
        approved: false,
        noConflicts: true,
        blockers: [],
      },
      additions: 890,
      deletions: 120,
    }),
  }),
];

// ── Review sessions
const reviewSessions: DashboardSession[] = [
  makeSession({
    id: "ses_v1",
    status: "ci_failed",
    activity: "idle",
    branch: "feat/search-index",
    pr: makePR({
      number: 151,
      title: "Implement full-text search with Elasticsearch",
      ciStatus: "failing",
      ciChecks: [
        { name: "build", status: "passed" },
        { name: "lint", status: "passed" },
        { name: "test", status: "failed" },
        { name: "e2e", status: "failed" },
      ],
      reviewDecision: "pending",
      mergeability: {
        mergeable: false,
        ciPassing: false,
        approved: false,
        noConflicts: true,
        blockers: [],
      },
      additions: 542,
      deletions: 89,
    }),
  }),
  makeSession({
    id: "ses_v2",
    status: "changes_requested",
    activity: "idle",
    branch: "refactor/api-layer",
    pr: makePR({
      number: 148,
      title: "Refactor API layer to use tRPC",
      ciStatus: "passing",
      reviewDecision: "changes_requested",
      mergeability: {
        mergeable: false,
        ciPassing: true,
        approved: false,
        noConflicts: false,
        blockers: [],
      },
      unresolvedThreads: 3,
      unresolvedComments: [
        {
          url: "#",
          path: "src/api/router.ts",
          author: "reviewer1",
          body: "This breaks backward compat",
        },
        {
          url: "#",
          path: "src/api/client.ts",
          author: "reviewer1",
          body: "Missing error handling",
        },
        { url: "#", path: "src/api/types.ts", author: "reviewer2", body: "Use zod schema instead" },
      ],
      additions: 1240,
      deletions: 680,
    }),
  }),
];

// ── Pending sessions
const pendingSessions: DashboardSession[] = [
  makeSession({
    id: "ses_p1",
    status: "review_pending",
    activity: "idle",
    branch: "feat/dark-mode",
    pr: makePR({
      number: 155,
      title: "Add comprehensive dark mode support",
      ciStatus: "pending",
      ciChecks: [
        { name: "build", status: "running" },
        { name: "test", status: "pending" },
      ],
      reviewDecision: "pending",
      mergeability: {
        mergeable: false,
        ciPassing: false,
        approved: false,
        noConflicts: true,
        blockers: [],
      },
      additions: 380,
      deletions: 95,
    }),
  }),
];

// ── Working sessions
const workingSessions: DashboardSession[] = [
  makeSession({
    id: "ses_w1",
    status: "working",
    activity: "active",
    branch: "feat/kanban-filters",
    summary: "Implementing column filters for kanban board views",
    pr: makePR({
      number: 162,
      title: "Add column filter controls to kanban board",
      ciStatus: "pending",
      reviewDecision: "none",
      mergeability: {
        mergeable: false,
        ciPassing: false,
        approved: false,
        noConflicts: true,
        blockers: [],
      },
      additions: 156,
      deletions: 22,
    }),
  }),
  makeSession({
    id: "ses_w2",
    status: "working",
    activity: "active",
    branch: "feat/sidebar-tree",
    summary: "Building collapsible project sidebar with health indicators",
  }),
  makeSession({
    id: "ses_w3",
    status: "working",
    activity: "active",
    branch: "feat/sse-events",
    summary: "Wiring real-time SSE event streaming for live updates",
  }),
];

// ── Done sessions
const doneSessions: DashboardSession[] = [
  makeSession({
    id: "ses_d1",
    status: "merged",
    activity: "exited",
    branch: "feat/theme-toggle",
    pr: makePR({
      number: 138,
      title: "Light/dark theme toggle with CSS custom properties",
      state: "merged",
      additions: 420,
      deletions: 180,
    }),
  }),
  makeSession({
    id: "ses_d2",
    status: "killed",
    activity: "exited",
    branch: "experiment/webgl-chart",
    summary: "Experimental WebGL chart rendering (abandoned)",
  }),
  makeSession({
    id: "ses_d3",
    status: "done",
    activity: "exited",
    branch: "fix/ssr-hydration",
    pr: makePR({
      number: 135,
      title: "Fix SSR hydration mismatch in theme provider",
      state: "merged",
      additions: 24,
      deletions: 8,
    }),
  }),
];

const allSessions = [
  ...mergeSessions,
  ...respondSessions,
  ...reviewSessions,
  ...pendingSessions,
  ...workingSessions,
  ...doneSessions,
];

const mockProjects: ProjectInfo[] = [
  { id: "proj_alpha", name: "Alpha" },
  { id: "proj_beta", name: "Beta" },
  { id: "proj_gamma", name: "Gamma" },
];

// ── Showcase Page ────────────────────────────────────────────────────────

const ZONE_ORDER: AttentionLevel[] = ["respond", "review", "pending", "working", "merge", "done"];
const zoneMap: Record<AttentionLevel, DashboardSession[]> = {
  merge: mergeSessions,
  respond: respondSessions,
  review: reviewSessions,
  pending: pendingSessions,
  working: workingSessions,
  done: doneSessions,
};

const ACTIVITY_STATES = ["active", "ready", "idle", "waiting_input", "blocked", "exited"] as const;
const CI_STATUSES: Array<{
  status: "passing" | "failing" | "pending" | "none";
  checks?: DashboardCICheck[];
}> = [
  {
    status: "passing",
    checks: [
      { name: "build", status: "passed" },
      { name: "test", status: "passed" },
    ],
  },
  {
    status: "failing",
    checks: [
      { name: "build", status: "passed" },
      { name: "test", status: "failed" },
    ],
  },
  {
    status: "pending",
    checks: [
      { name: "build", status: "running" },
      { name: "test", status: "pending" },
    ],
  },
  { status: "none" },
];

export default function ShowcasePage() {
  const [sidebarVisible, setSidebarVisible] = useState(true);
  const noop = () => {};
  const totalSessions = allSessions.length;
  const workingCount = workingSessions.length;
  const reviewLoad = respondSessions.length + reviewSessions.length + pendingSessions.length;
  const mergeReadyCount = mergeSessions.length;

  return (
    <div className="min-h-screen bg-[var(--color-bg-base)]">
      <div className="mx-auto max-w-[1480px] px-4 py-4 md:px-6 md:py-5">
        <section className="dashboard-hero mb-8">
          <div className="dashboard-hero__backdrop" />
          <div className="dashboard-hero__content">
            <div className="dashboard-hero__heading">
              <div className="dashboard-eyebrow">
                <span className="dashboard-eyebrow__dot" />
                Showcase reference
              </div>
              <div>
                <h1 className="dashboard-title">Dashboard UI Overhaul</h1>
                <p className="dashboard-subtitle">
                  Reference surface for the redesigned dark-mode kanban dashboard, including the
                  mission-control header, project rail, lane treatment, and card hierarchy.
                </p>
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                {[
                  "Mission Control Hero",
                  "Dark Kanban Lanes",
                  "Project Rail",
                  "Session Card States",
                  "CI / PR Signals",
                  "Theme Tokens",
                ].map((tag) => (
                  <span
                    key={tag}
                    className="rounded-full border border-[var(--color-border-subtle)] bg-[var(--color-tint-blue)] px-2.5 py-1 text-[11px] font-medium text-[var(--color-accent)]"
                  >
                    {tag}
                  </span>
                ))}
              </div>
            </div>

            <div className="dashboard-hero__meta">
              <div className="dashboard-stats">
                <span className="dashboard-stat">
                  <span className="dashboard-stat__value">{totalSessions}</span>
                  <span className="dashboard-stat__label">sessions</span>
                </span>
                <span className="dashboard-stat">
                  <span
                    className="dashboard-stat__value"
                    style={{ color: "var(--color-status-working)" }}
                  >
                    {workingCount}
                  </span>
                  <span className="dashboard-stat__label">working</span>
                </span>
                <span className="dashboard-stat">
                  <span
                    className="dashboard-stat__value"
                    style={{ color: "var(--color-status-attention)" }}
                  >
                    {reviewLoad}
                  </span>
                  <span className="dashboard-stat__label">attention load</span>
                </span>
                <span className="dashboard-stat">
                  <span
                    className="dashboard-stat__value"
                    style={{ color: "var(--color-status-ready)" }}
                  >
                    {mergeReadyCount}
                  </span>
                  <span className="dashboard-stat__label">merge ready</span>
                </span>
              </div>
              <div className="flex items-center gap-3">
                <button
                  onClick={() => setSidebarVisible(!sidebarVisible)}
                  className="orchestrator-btn rounded-[7px] px-4 py-2 text-[12px] font-semibold"
                >
                  {sidebarVisible ? "Hide" : "Show"} sidebar
                </button>
                <ThemeToggle />
              </div>
            </div>
          </div>
        </section>

        {/* ── Section: Kanban Board ──────────────────────────── */}
        <Section
          title="Kanban Board"
          subtitle="Primary reference view for the redesign, matching the live dashboard’s dark-mode structure"
        >
          <div className="overflow-hidden border border-[var(--color-border-default)] bg-[var(--color-bg-surface)]">
            <div className="flex items-center justify-between gap-4 border-b border-[var(--color-border-subtle)] px-5 py-3">
              <div>
                <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--color-accent)]">
                  Live board shell
                </div>
                <div className="mt-1 text-[13px] text-[var(--color-text-secondary)]">
                  Session lanes, sidebar, and card hierarchy as they should look in the app.
                </div>
              </div>
              <div className="flex flex-wrap gap-2 text-[11px] text-[var(--color-text-muted)]">
                <span className="rounded-full border border-[var(--color-border-subtle)] px-2.5 py-1">
                  Graphite surface
                </span>
                <span className="rounded-full border border-[var(--color-border-subtle)] px-2.5 py-1">
                  Bright status accents
                </span>
                <span className="rounded-full border border-[var(--color-border-subtle)] px-2.5 py-1">
                  Independent lane scroll
                </span>
              </div>
            </div>

            <div className="dashboard-shell flex min-h-[760px]">
              {sidebarVisible && (
                <div className="shrink-0">
                  <ProjectSidebar
                    projects={mockProjects}
                    sessions={allSessions}
                    activeProjectId="proj_alpha"
                    activeSessionId={undefined}
                  />
                </div>
              )}

              <div className="dashboard-main flex-1 overflow-hidden px-4 py-4 md:px-6 md:py-5">
                <div className="kanban-board-wrap">
                  <div className="kanban-board">
                    {ZONE_ORDER.map((level) => (
                      <AttentionZone
                        key={level}
                        level={level}
                        sessions={zoneMap[level]}
                        onSend={noop}
                        onKill={noop}
                        onMerge={noop}
                        onRestore={noop}
                      />
                    ))}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </Section>

        {/* ── Section: Attention Zones ───────────────────────── */}
        <Section
          title="Attention Zones"
          subtitle="The redesigned lane language: explicit title, count, and short operator caption"
        >
          <div className="grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6">
            {(
              [
                {
                  level: "merge",
                  label: "Merge Ready",
                  color: "var(--color-status-ready)",
                  desc: "PR approved + CI green",
                },
                {
                  level: "respond",
                  label: "Respond",
                  color: "var(--color-status-error)",
                  desc: "Agent waiting for input",
                },
                {
                  level: "review",
                  label: "Review",
                  color: "var(--color-accent-orange)",
                  desc: "CI failed / changes requested",
                },
                {
                  level: "pending",
                  label: "Pending",
                  color: "var(--color-status-attention)",
                  desc: "Waiting on reviewer / CI",
                },
                {
                  level: "working",
                  label: "Working",
                  color: "var(--color-status-working)",
                  desc: "Agent actively coding",
                },
                {
                  level: "done",
                  label: "Done",
                  color: "var(--color-text-tertiary)",
                  desc: "Merged or terminated",
                },
              ] as const
            ).map((zone) => (
              <div
                key={zone.level}
                className="border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-4"
              >
                <div className="mb-3 flex items-center gap-2">
                  <div
                    className="h-2.5 w-2.5 rounded-full shadow-[0_0_18px_currentColor]"
                    style={{ background: zone.color }}
                  />
                  <span className="text-[12px] font-semibold text-[var(--color-text-primary)]">
                    {zone.label}
                  </span>
                </div>
                <div className="mb-2 text-[10px] uppercase tracking-[0.12em] text-[var(--color-text-tertiary)]">
                  Lane rule
                </div>
                <p className="text-[11px] leading-relaxed text-[var(--color-text-muted)]">
                  {zone.desc}
                </p>
              </div>
            ))}
          </div>
        </Section>

        {/* ── Section: Activity States ───────────────────────── */}
        <Section
          title="Activity Indicators"
          subtitle="Real-time activity dots and pills showing agent state"
        >
          <div className="flex flex-wrap items-center gap-4">
            {ACTIVITY_STATES.map((activity) => (
              <div key={activity} className="flex items-center gap-3">
                <ActivityDot activity={activity} />
                <ActivityDot activity={activity} dotOnly size={8} />
              </div>
            ))}
          </div>
        </Section>

        {/* ── Section: CI Badges ─────────────────────────────── */}
        <Section
          title="CI Status Badges"
          subtitle="CI check status indicators with inline and expanded views"
        >
          <div className="flex flex-wrap items-center gap-4">
            {CI_STATUSES.map((ci) => (
              <CIBadge key={ci.status} status={ci.status} checks={ci.checks} />
            ))}
          </div>
        </Section>

        {/* ── Section: Card Variants ─────────────────────────── */}
        <Section
          title="Session Card Variants"
          subtitle="Reference cards for each major state in the redesigned board"
        >
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            {/* Merge-ready card */}
            <div>
              <Label>Merge Ready</Label>
              <SessionCard
                session={mergeSessions[0]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>

            {/* Respond card */}
            <div>
              <Label>Needs Response</Label>
              <SessionCard
                session={respondSessions[0]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>

            {/* Review card with CI failures */}
            <div>
              <Label>Review (CI Failing)</Label>
              <SessionCard
                session={reviewSessions[0]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>

            {/* Review card with unresolved comments */}
            <div>
              <Label>Review (Changes Requested)</Label>
              <SessionCard
                session={reviewSessions[1]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>

            {/* Working card */}
            <div>
              <Label>Working</Label>
              <SessionCard
                session={workingSessions[0]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>

            {/* Done card (merged) */}
            <div>
              <Label>Done (Merged)</Label>
              <SessionCard
                session={doneSessions[0]}
                onSend={noop}
                onKill={noop}
                onMerge={noop}
                onRestore={noop}
              />
            </div>
          </div>
        </Section>

        {/* ── Section: Design Tokens ─────────────────────────── */}
        <Section
          title="Design System"
          subtitle="CSS custom property tokens powering the entire UI — toggle theme to see both palettes"
        >
          {/* Colors */}
          <div className="mb-6">
            <Label>Status Colors</Label>
            <div className="flex flex-wrap gap-3">
              {[
                { name: "Working", var: "--color-status-working" },
                { name: "Ready", var: "--color-status-ready" },
                { name: "Attention", var: "--color-status-attention" },
                { name: "Error", var: "--color-status-error" },
                { name: "Done", var: "--color-status-done" },
                { name: "Idle", var: "--color-status-idle" },
              ].map((c) => (
                <div key={c.var} className="flex items-center gap-2">
                  <div
                    className="h-6 w-6 rounded-full border border-[var(--color-border-default)]"
                    style={{ background: `var(${c.var})` }}
                  />
                  <div>
                    <div className="text-[11px] font-medium text-[var(--color-text-primary)]">
                      {c.name}
                    </div>
                    <div className="font-mono text-[9px] text-[var(--color-text-muted)]">
                      {c.var}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="mb-6">
            <Label>Accent Colors</Label>
            <div className="flex flex-wrap gap-3">
              {[
                { name: "Blue", var: "--color-accent-blue" },
                { name: "Green", var: "--color-accent-green" },
                { name: "Yellow", var: "--color-accent-yellow" },
                { name: "Orange", var: "--color-accent-orange" },
                { name: "Red", var: "--color-accent-red" },
                { name: "Violet", var: "--color-accent-violet" },
              ].map((c) => (
                <div key={c.var} className="flex items-center gap-2">
                  <div
                    className="h-6 w-6 rounded-full border border-[var(--color-border-default)]"
                    style={{ background: `var(${c.var})` }}
                  />
                  <div>
                    <div className="text-[11px] font-medium text-[var(--color-text-primary)]">
                      {c.name}
                    </div>
                    <div className="font-mono text-[9px] text-[var(--color-text-muted)]">
                      {c.var}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="mb-6">
            <Label>Surfaces</Label>
            <div className="flex flex-wrap gap-3">
              {[
                { name: "Base", var: "--color-bg-base" },
                { name: "Surface", var: "--color-bg-surface" },
                { name: "Elevated", var: "--color-bg-elevated" },
                { name: "Subtle", var: "--color-bg-subtle" },
              ].map((c) => (
                <div key={c.var} className="flex items-center gap-2">
                  <div
                    className="h-6 w-12 rounded border border-[var(--color-border-default)]"
                    style={{ background: `var(${c.var})` }}
                  />
                  <div>
                    <div className="text-[11px] font-medium text-[var(--color-text-primary)]">
                      {c.name}
                    </div>
                    <div className="font-mono text-[9px] text-[var(--color-text-muted)]">
                      {c.var}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="mb-6">
            <Label>Tint Backgrounds</Label>
            <div className="flex flex-wrap gap-3">
              {[
                { name: "Blue", var: "--color-tint-blue" },
                { name: "Green", var: "--color-tint-green" },
                { name: "Yellow", var: "--color-tint-yellow" },
                { name: "Red", var: "--color-tint-red" },
                { name: "Violet", var: "--color-tint-violet" },
                { name: "Orange", var: "--color-tint-orange" },
                { name: "Neutral", var: "--color-tint-neutral" },
              ].map((c) => (
                <div key={c.var} className="flex items-center gap-2">
                  <div
                    className="h-6 w-12 rounded border border-[var(--color-border-default)]"
                    style={{ background: `var(${c.var})` }}
                  />
                  <div>
                    <div className="text-[11px] font-medium text-[var(--color-text-primary)]">
                      {c.name}
                    </div>
                    <div className="font-mono text-[9px] text-[var(--color-text-muted)]">
                      {c.var}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* Typography */}
          <div className="mb-6">
            <Label>Typography</Label>
            <div className="space-y-3 rounded-[8px] border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-5">
              <div>
                <span className="text-[17px] font-bold tracking-[-0.02em] text-[var(--color-text-primary)]">
                  Heading XL — IBM Plex Sans 17px/700
                </span>
              </div>
              <div>
                <span className="text-[14px] font-semibold text-[var(--color-text-primary)]">
                  Heading — IBM Plex Sans 14px/600
                </span>
              </div>
              <div>
                <span className="text-[13px] text-[var(--color-text-primary)]">
                  Body — IBM Plex Sans 13px/400
                </span>
              </div>
              <div>
                <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--color-text-secondary)]">
                  Label — IBM Plex Sans 11px/600 uppercase
                </span>
              </div>
              <div>
                <span className="font-mono text-[11px] text-[var(--color-text-muted)]">
                  Monospace — IBM Plex Mono 11px
                </span>
              </div>
            </div>
          </div>
        </Section>

        {/* ── Section: Animations ────────────────────────────── */}
        <Section
          title="Animations"
          subtitle="Smooth transitions and micro-interactions throughout the UI"
        >
          <div className="grid gap-6 md:grid-cols-3">
            {/* Activity pulse */}
            <div className="rounded-[8px] border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-5">
              <Label>Activity Pulse</Label>
              <p className="mb-3 text-[11px] text-[var(--color-text-muted)]">
                Active agents show a pulsing glow effect
              </p>
              <div className="flex items-center gap-4">
                <div
                  className="h-3 w-3 rounded-full animate-[activity-pulse_2s_ease-in-out_infinite]"
                  style={{ background: "var(--color-status-working)" }}
                />
                <div
                  className="h-3 w-3 rounded-full animate-[activity-pulse_2s_ease-in-out_infinite]"
                  style={{ background: "var(--color-status-error)" }}
                />
                <div
                  className="h-3 w-3 rounded-full animate-[activity-pulse_2s_ease-in-out_infinite]"
                  style={{ background: "var(--color-status-ready)" }}
                />
              </div>
            </div>

            {/* Page enter */}
            <div className="rounded-[8px] border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-5">
              <Label>Page Enter</Label>
              <p className="mb-3 text-[11px] text-[var(--color-text-muted)]">
                Pages slide up on entry for smooth navigation
              </p>
              <div className="page-enter text-[12px] text-[var(--color-text-secondary)]">
                Content slides in from below
              </div>
            </div>

            {/* Skeleton loading */}
            <div className="rounded-[8px] border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-5">
              <Label>Loading Skeleton</Label>
              <p className="mb-3 text-[11px] text-[var(--color-text-muted)]">
                Shimmer effect for loading states
              </p>
              <div className="space-y-2">
                <div className="h-3 w-3/4 animate-pulse rounded bg-[var(--color-border-subtle)]" />
                <div className="h-3 w-1/2 animate-pulse rounded bg-[var(--color-border-subtle)]" />
                <div className="h-3 w-5/6 animate-pulse rounded bg-[var(--color-border-subtle)]" />
              </div>
            </div>
          </div>
        </Section>

        {/* ── Section: Alert Banners ─────────────────────────── */}
        <Section title="Alert Banners" subtitle="Contextual alerts for system-wide events">
          <div className="space-y-4">
            <div className="flex items-center gap-2.5 rounded border border-[color-mix(in_srgb,var(--color-status-error)_25%,transparent)] bg-[var(--color-tint-red)] px-3.5 py-2.5 text-[11px] text-[var(--color-status-error)]">
              <svg
                className="h-3.5 w-3.5 shrink-0"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                viewBox="0 0 24 24"
              >
                <circle cx="12" cy="12" r="10" />
                <path d="M12 8v4M12 16h.01" />
              </svg>
              <span className="flex-1">
                <strong>Orchestrator paused:</strong> Rate limit exceeded. Resume after 2:30 PM
              </span>
            </div>

            <div className="flex items-center gap-2.5 rounded border border-[color-mix(in_srgb,var(--color-status-attention)_25%,transparent)] bg-[var(--color-tint-yellow)] px-3.5 py-2.5 text-[11px] text-[var(--color-status-attention)]">
              <svg
                className="h-3.5 w-3.5 shrink-0"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                viewBox="0 0 24 24"
              >
                <circle cx="12" cy="12" r="10" />
                <path d="M12 8v4M12 16h.01" />
              </svg>
              <span className="flex-1">
                GitHub API rate limited — PR data (CI status, review state, sizes) may be stale.
              </span>
            </div>
          </div>
        </Section>

        {/* ── Section: Theme Comparison ──────────────────────── */}
        <Section
          title="Theme Support"
          subtitle="Toggle the theme button in the top-right to switch between light and dark modes"
        >
          <div className="rounded-[8px] border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-6">
            <div className="flex items-center gap-4">
              <ThemeToggle />
              <div>
                <p className="text-[13px] font-medium text-[var(--color-text-primary)]">
                  Full Light / Dark Mode
                </p>
                <p className="text-[11px] text-[var(--color-text-muted)]">
                  50+ CSS custom property tokens with smooth 0.25s transitions. Theme persists via
                  localStorage. FOUC prevention with beforeInteractive script.
                </p>
              </div>
            </div>
          </div>
        </Section>

        {/* ── Footer ─────────────────────────────────────────── */}
        <footer className="mt-16 border-t border-[var(--color-border-subtle)] pt-6 pb-12 text-center">
          <p className="text-[11px] text-[var(--color-text-muted)]">
            Agent Orchestrator — Dashboard UI Overhaul Showcase
          </p>
        </footer>
      </div>
    </div>
  );
}

// ── Helper Components ────────────────────────────────────────────────────

function Section({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
}) {
  return (
    <section className="mb-14">
      <div className="mb-5">
        <h2 className="text-[17px] font-bold tracking-[-0.02em] text-[var(--color-text-primary)]">
          {title}
        </h2>
        <p className="mt-1 text-[13px] text-[var(--color-text-muted)]">{subtitle}</p>
      </div>
      {children}
    </section>
  );
}

function Label({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-2 text-[10px] font-bold uppercase tracking-[0.10em] text-[var(--color-text-tertiary)]">
      {children}
    </div>
  );
}
