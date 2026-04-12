import { afterEach, describe, expect, it, vi } from "vitest";
import { generateOrchestratorPrompt } from "../orchestrator-prompt.js";
import type * as NodeFsModule from "node:fs";
import type { OrchestratorConfig, ProjectConfig } from "../types.js";

const config: OrchestratorConfig = {
  configPath: "/tmp/agent-orchestrator.yaml",
  port: 3000,
  defaults: {
    runtime: "tmux",
    agent: "claude-code",
    workspace: "worktree",
    notifiers: ["desktop"],
  },
  projects: {
    "my-app": {
      name: "My App",
      repo: "org/my-app",
      path: "/tmp/my-app",
      defaultBranch: "main",
      sessionPrefix: "app",
    },
  },
  notifiers: {},
  notificationRouting: {
    urgent: ["desktop"],
    action: ["desktop"],
    warning: [],
    info: [],
  },
  reactions: {},
  readyThresholdMs: 300_000,
};

describe("generateOrchestratorPrompt", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.doUnmock("node:fs");
    vi.resetModules();
  });

  it("requires read-only investigation from the orchestrator session", () => {
    const prompt = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: config.projects["my-app"]!,
    });

    expect(prompt).toContain("Investigations from the orchestrator session are **read-only**");
    expect(prompt).toContain("do not edit repository files or implement fixes");
  });

  it("mandates ao send and bans raw tmux access", () => {
    const prompt = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: config.projects["my-app"]!,
    });

    expect(prompt).toContain("Always use `ao send`");
    expect(prompt).toContain("never use raw `tmux send-keys`");
    expect(prompt).toContain("ao send --no-wait");
  });

  it("pushes implementation and PR claiming into worker sessions", () => {
    const prompt = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: config.projects["my-app"]!,
    });

    expect(prompt).toContain("must be delegated to a **worker session**");
    expect(prompt).toContain("Never claim a PR into `app-orchestrator`");
    expect(prompt).toContain("Delegate implementation, test execution, or PR claiming");
  });

  it("expands markdown template placeholders with typed render data", () => {
    const prompt = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: config.projects["my-app"]!,
    });

    expect(prompt).toContain("# My App Orchestrator");
    expect(prompt).toContain("- **Repository**: org/my-app");
    expect(prompt).toContain("ao session ls -p my-app");
    expect(prompt).toContain("http://localhost:3000");
  });

  it("throws when the markdown template contains an unresolved placeholder", async () => {
    vi.doMock("node:fs", async () => {
      const actual = await vi.importActual<typeof NodeFsModule>("node:fs");

      return {
        ...actual,
        readFileSync: vi.fn(() => "Hello {{missingPlaceholder}}"),
      };
    });

    const { generateOrchestratorPrompt: generateWithMockedTemplate } =
      await import("../orchestrator-prompt.js");

    expect(() =>
      generateWithMockedTemplate({
        config,
        projectId: "my-app",
        project: config.projects["my-app"]!,
      }),
    ).toThrow("Unresolved template placeholder: missingPlaceholder");
  });

  it("renders optional sections only when project data is present", () => {
    const projectWithOptionalSections: ProjectConfig = {
      ...config.projects["my-app"]!,
      reactions: {
        ci_failed: {
          auto: true,
          action: "send-to-agent",
          retries: 2,
          escalateAfter: 3,
        },
      },
      orchestratorRules: "Escalate production incidents immediately.",
    };

    const promptWithOptionalSections = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: projectWithOptionalSections,
    });

    const promptWithoutOptionalSections = generateOrchestratorPrompt({
      config,
      projectId: "my-app",
      project: config.projects["my-app"]!,
    });

    expect(promptWithOptionalSections).toContain("## Automated Reactions");
    expect(promptWithOptionalSections).toContain("**ci_failed**");
    expect(promptWithOptionalSections).toContain("## Project-Specific Rules");
    expect(promptWithOptionalSections).toContain("Escalate production incidents immediately.");
    expect(promptWithoutOptionalSections).not.toContain("## Automated Reactions");
    expect(promptWithoutOptionalSections).not.toContain("## Project-Specific Rules");
  });
});
