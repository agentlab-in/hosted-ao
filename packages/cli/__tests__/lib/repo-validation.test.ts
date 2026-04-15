import { describe, it, expect } from "vitest";

/**
 * Tests for the repo validation regex used in autoCreateConfig and addProjectToConfig.
 * The regex is inlined in start.ts — this test validates the pattern independently.
 */
const REPO_REGEX = /^[^\s/]+\/[^\s/]+$/;

describe("repo validation regex", () => {
  it("accepts valid owner/repo", () => {
    expect(REPO_REGEX.test("acme/my-app")).toBe(true);
    expect(REPO_REGEX.test("ComposioHQ/agent-orchestrator")).toBe(true);
    expect(REPO_REGEX.test("org/repo")).toBe(true);
  });

  it("rejects empty string", () => {
    expect(REPO_REGEX.test("")).toBe(false);
  });

  it("rejects lone slash", () => {
    expect(REPO_REGEX.test("/")).toBe(false);
  });

  it("rejects missing owner", () => {
    expect(REPO_REGEX.test("/repo")).toBe(false);
  });

  it("rejects missing repo name", () => {
    expect(REPO_REGEX.test("owner/")).toBe(false);
  });

  it("rejects strings with whitespace", () => {
    expect(REPO_REGEX.test("acme/repo extra")).toBe(false);
    expect(REPO_REGEX.test("acme /repo")).toBe(false);
  });

  it("rejects nested paths (more than one slash)", () => {
    expect(REPO_REGEX.test("acme/repo/extra")).toBe(false);
  });

  it("rejects strings without a slash", () => {
    expect(REPO_REGEX.test("notaslash")).toBe(false);
  });

  it("rejects strings with spaces in segments", () => {
    expect(REPO_REGEX.test("my org/repo")).toBe(false);
    expect(REPO_REGEX.test("org/my repo")).toBe(false);
  });
});
