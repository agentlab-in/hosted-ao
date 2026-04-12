/**
 * Orchestrator Prompt Generator - generates orchestrator prompt content.
 *
 * This is injected via `ao start` to provide orchestrator-specific context
 * when the orchestrator agent runs.
 */

import * as fs from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { OrchestratorConfig, ProjectConfig } from "./types.js";

export interface OrchestratorPromptConfig {
  config: OrchestratorConfig;
  projectId: string;
  project: ProjectConfig;
}

interface OrchestratorPromptRenderData {
  projectId: string;
  projectName: string;
  projectRepo: string;
  projectDefaultBranch: string;
  projectSessionPrefix: string;
  projectPath: string;
  dashboardPort: string;
  automatedReactionsSection: string;
  projectSpecificRulesSection: string;
}

const moduleDir = dirname(fileURLToPath(import.meta.url));
const ORCHESTRATOR_TEMPLATE_PATHS = [
  join(moduleDir, "prompts", "orchestrator.md"),
  join(moduleDir, "..", "src", "prompts", "orchestrator.md"),
];

function isErrnoException(error: unknown): error is NodeJS.ErrnoException {
  return error instanceof Error && "code" in error;
}

function loadOrchestratorTemplate(): string {
  for (const templatePath of ORCHESTRATOR_TEMPLATE_PATHS) {
    try {
      return fs.readFileSync(templatePath, "utf-8").trim();
    } catch (error) {
      if (isErrnoException(error) && error.code === "ENOENT") {
        continue;
      }
      throw error;
    }
  }

  throw new Error(
    `Unable to find orchestrator prompt template. Checked: ${ORCHESTRATOR_TEMPLATE_PATHS.join(", ")}`,
  );
}

function buildAutomatedReactionsSection(project: ProjectConfig): string {
  const reactionLines: string[] = [];

  for (const [event, reaction] of Object.entries(project.reactions ?? {})) {
    if (reaction.auto && reaction.action === "send-to-agent") {
      reactionLines.push(
        `- **${event}**: Auto-sends instruction to agent (retries: ${reaction.retries ?? "none"}, escalates after: ${reaction.escalateAfter ?? "never"})`,
      );
      continue;
    }

    if (reaction.auto && reaction.action === "notify") {
      reactionLines.push(
        `- **${event}**: Notifies human (priority: ${reaction.priority ?? "info"})`,
      );
    }
  }

  if (reactionLines.length === 0) {
    return "";
  }

  return `## Automated Reactions

The system automatically handles these events:

${reactionLines.join("\n")}`;
}

function buildProjectSpecificRulesSection(project: ProjectConfig): string {
  const rules = project.orchestratorRules?.trim();
  if (!rules) {
    return "";
  }

  return `## Project-Specific Rules

${rules}`;
}

function createRenderData(opts: OrchestratorPromptConfig): OrchestratorPromptRenderData {
  const { config, projectId, project } = opts;

  return {
    projectId,
    projectName: project.name,
    projectRepo: project.repo,
    projectDefaultBranch: project.defaultBranch,
    projectSessionPrefix: project.sessionPrefix,
    projectPath: project.path,
    dashboardPort: String(config.port ?? 3000),
    automatedReactionsSection: buildAutomatedReactionsSection(project),
    projectSpecificRulesSection: buildProjectSpecificRulesSection(project),
  };
}

function renderTemplate(template: string, data: OrchestratorPromptRenderData): string {
  return template.replace(/\{\{([a-zA-Z0-9]+)\}\}/g, (_match, rawKey: string) => {
    if (!(rawKey in data)) {
      throw new Error(`Unresolved template placeholder: ${rawKey}`);
    }

    return data[rawKey as keyof OrchestratorPromptRenderData];
  });
}

function normalizeRenderedPrompt(prompt: string): string {
  return prompt.replace(/\n{3,}/g, "\n\n").trim();
}

/**
 * Generate orchestrator prompt content.
 * Provides orchestrator agent with context about available commands,
 * session management workflows, and project configuration.
 */
export function generateOrchestratorPrompt(opts: OrchestratorPromptConfig): string {
  return normalizeRenderedPrompt(
    renderTemplate(loadOrchestratorTemplate(), createRenderData(opts)),
  );
}
