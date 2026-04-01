import { describe, expect, it } from "vitest";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import packageJson from "../../package.json" with { type: "json" };

describe("ao --version", () => {
  it("matches the CLI package version", () => {
    const tsxEntry = fileURLToPath(new URL("../../node_modules/.bin/tsx", import.meta.url));
    const cliEntry = fileURLToPath(new URL("../../src/index.ts", import.meta.url));
    const output = execFileSync(tsxEntry, [cliEntry, "--version"], { encoding: "utf8" }).trim();

    expect(output).toBe(packageJson.version);
  });
});
