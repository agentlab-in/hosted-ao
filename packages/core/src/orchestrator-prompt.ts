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

type OrchestratorPromptRenderKey = keyof OrchestratorPromptRenderData;

const moduleDir = dirname(fileURLToPath(import.meta.url));
const ORCHESTRATOR_PROMPT_DIR = "prompts";
const ORCHESTRATOR_PROMPT_TEMPLATE = "orchestrator.md";
const ORCHESTRATOR_TEMPLATE_PATHS = [
  join(moduleDir, ORCHESTRATOR_PROMPT_DIR, ORCHESTRATOR_PROMPT_TEMPLATE),
  join(moduleDir, "..", "src", ORCHESTRATOR_PROMPT_DIR, ORCHESTRATOR_PROMPT_TEMPLATE),
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
  const markdownBold = String.fromCharCode(42).repeat(2);
  const bold = (text: string): string => `${markdownBold}${text}${markdownBold}`;

  const reactionLines: string[] = [];

  for (const [event, reaction] of Object.entries(project.reactions ?? {})) {
    if (reaction.auto && reaction.action === "send-to-agent") {
      reactionLines.push(
        `- ${bold(event)}: Auto-sends instruction to agent (retries: ${reaction.retries ?? "none"}, escalates after: ${reaction.escalateAfter ?? "never"})`,
      );
      continue;
    }

    if (reaction.auto && reaction.action === "notify") {
      reactionLines.push(
        `- ${bold(event)}: Notifies human (priority: ${reaction.priority ?? "info"})`,
      );
    }
  }

  if (reactionLines.length === 0) {
    return "";
  }

  return reactionLines.join("\n");
}

function buildProjectSpecificRulesSection(project: ProjectConfig): string {
  const rules = project.orchestratorRules?.trim();
  if (!rules) {
    return "";
  }

  return rules;
}

function removeOptionalSectionBlocks(
  template: string,
  data: OrchestratorPromptRenderData,
): string {
  const templates = [
    ["AUTOMATED_REACTIONS_SECTION_START", "AUTOMATED_REACTIONS_SECTION_END", data.automatedReactionsSection],
    ["PROJECT_SPECIFIC_RULES_SECTION_START", "PROJECT_SPECIFIC_RULES_SECTION_END", data.projectSpecificRulesSection],
  ] as const;

  let interpolated = template;
  for (const [startKey, endKey, section] of templates) {
    const startMarker = `{{${startKey}}}`;
    const endMarker = `{{${endKey}}}`;
    const start = interpolated.indexOf(startMarker);
    const end = interpolated.indexOf(endMarker);

    if (start === -1 && end === -1) {
      continue;
    }

    if (start === -1 || end === -1 || end < start) {
      throw new Error(
        `Malformed optional section block: expected ${startMarker} before ${endMarker}`,
      );
    }

    const fullStart = start;
    const fullEnd = end + endMarker.length;
    const blockContent = interpolated.slice(start + startMarker.length, end);
    const replacement = section ? blockContent : "";

    interpolated =
      interpolated.slice(0, fullStart) +
      replacement +
      interpolated.slice(fullEnd);
  }

  return interpolated;
}

function hasRenderDataKey(
  data: OrchestratorPromptRenderData,
  key: string,
): key is OrchestratorPromptRenderKey {
  return Object.prototype.hasOwnProperty.call(data, key);
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
  return template.replace(/\{\{([a-zA-Z0-9_]+)\}\}/g, (_match, rawKey: string) => {
    if (!hasRenderDataKey(data, rawKey)) {
      throw new Error(`Unresolved template placeholder: ${rawKey}`);
    }

    return data[rawKey];
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
  const data = createRenderData(opts);
  const template = loadOrchestratorTemplate();
  const templateWithOptionalSections = removeOptionalSectionBlocks(template, data);

  return normalizeRenderedPrompt(
    renderTemplate(templateWithOptionalSections, data),
  );
}
