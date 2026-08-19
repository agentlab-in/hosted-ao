import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";

const repositoryRoot = path.resolve(import.meta.dirname, "..");

describe("desktop release workflows", () => {
  it("build-artifacts.yml requires the WorkOS client ID", async () => {
    const contents = await readFile(
      path.join(repositoryRoot, ".github", "workflows", "build-artifacts.yml"),
      "utf8",
    );

    expect(contents).toContain(
      "VITE_WORKOS_CLIENT_ID: ${{ vars.VITE_WORKOS_CLIENT_ID }}",
    );
    expect(contents).toContain(
      "Repository variable VITE_WORKOS_CLIENT_ID is required",
    );
  });

  // frontend-release.yml is the one authorized publisher (AGENTS.md), and this
  // fork pins AO Cloud sign-in off permanently (frontend/src/shared/cloud-pin.ts),
  // so a missing WorkOS client ID must not block a release: it guards a sign-in
  // surface the fork's builds hide end to end.
  it("frontend-release.yml tolerates a missing WorkOS client ID (AO Cloud sign-in is pinned off in this fork)", async () => {
    const contents = await readFile(
      path.join(repositoryRoot, ".github", "workflows", "frontend-release.yml"),
      "utf8",
    );

    expect(contents).toContain(
      "VITE_WORKOS_CLIENT_ID: ${{ vars.VITE_WORKOS_CLIENT_ID }}",
    );
    expect(contents).toContain(
      "AO Cloud sign-in is pinned off in this fork",
    );
    expect(contents).not.toContain(
      "Repository variable VITE_WORKOS_CLIENT_ID is required",
    );
  });
});
