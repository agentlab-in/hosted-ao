/**
 * Parses Vitest JSON coverage reports, filters to PR-changed files,
 * and writes a Markdown summary to coverage-comment.md.
 *
 * Expects:
 *   - changed-files.txt in cwd (one relative path per line)
 *   - coverage-final.json in each package's coverage/ directory
 *
 * Sets GITHUB_OUTPUT: has_coverage=true|false
 */

/* eslint-disable no-undef -- Node.js CI script; process/console are globals */
import { readFileSync, writeFileSync, existsSync, appendFileSync, realpathSync, readdirSync } from "node:fs";
import { resolve, relative } from "node:path";

const COMMENT_TAG = "<!-- coverage-report -->";
const cwd = realpathSync(process.cwd());
const ghOutput = process.env.GITHUB_OUTPUT;

// ── 1. Read changed files ──────────────────────────────────────────
const changedFiles = readFileSync("changed-files.txt", "utf-8")
  .split("\n")
  .map((f) => f.trim())
  .filter((f) => f && (f.endsWith(".ts") || f.endsWith(".tsx")))
  .filter((f) => !f.includes("__tests__") && !f.includes(".test."));

if (changedFiles.length === 0) {
  const comment = `${COMMENT_TAG}\n## Test Coverage Report\n\n_No TypeScript source files changed in this PR._\n`;
  writeFileSync("coverage-comment.md", comment);
  if (ghOutput) appendFileSync(ghOutput, "has_coverage=false\n");
  console.log(comment);
  process.exit(0);
}

// ── 2. Discover coverage-final.json files ──────────────────────────
function findCoverageFiles(baseDir) {
  const results = [];
  const packagesDir = resolve(baseDir, "packages");

  function walk(dir) {
    let entries;
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (entry.name === "node_modules") continue;
      const full = resolve(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === "coverage") {
          const jsonFile = resolve(full, "coverage-final.json");
          if (existsSync(jsonFile)) results.push(jsonFile);
        } else {
          walk(full);
        }
      }
    }
  }

  walk(packagesDir);
  return results;
}

const coverageFiles = findCoverageFiles(cwd);

// ── 3. Parse coverage and filter to changed files ──────────────────
let totalLines = 0;
let coveredLines = 0;
const fileReports = [];

for (const jsonPath of coverageFiles) {
  const coverage = JSON.parse(readFileSync(jsonPath, "utf-8"));

  for (const [absPath, data] of Object.entries(coverage)) {
    // Normalize to handle symlinks (e.g. /tmp -> /private/tmp on macOS)
    const realAbsPath = existsSync(absPath) ? realpathSync(absPath) : absPath;
    const relPath = relative(cwd, realAbsPath);

    if (!changedFiles.includes(relPath)) continue;

    // Derive per-line hit counts from the statement map.
    // A line is "covered" if any statement on it was executed.
    const lineHits = new Map();

    for (const [id, loc] of Object.entries(data.statementMap)) {
      const hits = data.s[id] ?? 0;
      for (let line = loc.start.line; line <= loc.end.line; line++) {
        const prev = lineHits.get(line);
        // Keep the highest hit count for each line
        if (prev === undefined || hits > prev) {
          lineHits.set(line, hits);
        }
      }
    }

    const fileTotalLines = lineHits.size;
    const fileCoveredLines = [...lineHits.values()].filter((h) => h > 0).length;
    const uncoveredLineNums = [...lineHits.entries()]
      .filter(([, h]) => h === 0)
      .map(([l]) => l)
      .sort((a, b) => a - b);

    totalLines += fileTotalLines;
    coveredLines += fileCoveredLines;

    if (fileTotalLines > 0) {
      fileReports.push({
        path: relPath,
        total: fileTotalLines,
        covered: fileCoveredLines,
        pct: ((fileCoveredLines / fileTotalLines) * 100).toFixed(1),
        uncoveredLines: uncoveredLineNums,
      });
    }
  }
}

// ── 4. Build Markdown comment ──────────────────────────────────────

/** Collapse consecutive line numbers into ranges: [1,2,3,7,9,10] -> "L1-L3, L7, L9-L10" */
function consolidateRanges(lines) {
  if (lines.length === 0) return "";
  const ranges = [];
  let start = lines[0];
  let end = lines[0];

  for (let i = 1; i < lines.length; i++) {
    if (lines[i] === end + 1) {
      end = lines[i];
    } else {
      ranges.push(start === end ? `L${start}` : `L${start}-L${end}`);
      start = lines[i];
      end = lines[i];
    }
  }
  ranges.push(start === end ? `L${start}` : `L${start}-L${end}`);
  return ranges.join(", ");
}

let comment = `${COMMENT_TAG}\n## Test Coverage Report\n\n`;

if (fileReports.length === 0) {
  comment +=
    "_Changed files have no coverage data (not instrumented or no tests ran)._\n";
  if (ghOutput) appendFileSync(ghOutput, "has_coverage=false\n");
} else {
  const pct =
    totalLines > 0 ? ((coveredLines / totalLines) * 100).toFixed(1) : "0.0";
  const uncoveredTotal = totalLines - coveredLines;

  comment += "| Metric | Value |\n";
  comment += "|--------|-------|\n";
  comment += `| Lines covered | ${coveredLines}/${totalLines} |\n`;
  comment += `| Lines not covered | ${uncoveredTotal}/${totalLines} |\n`;
  comment += `| Overall coverage | ${pct}% |\n\n`;

  // Per-file breakdown
  if (fileReports.length > 1) {
    comment += "<details>\n<summary>Per-file breakdown</summary>\n\n";
    comment += "| File | Coverage |\n";
    comment += "|------|----------|\n";
    for (const f of fileReports.sort((a, b) => a.path.localeCompare(b.path))) {
      comment += `| \`${f.path}\` | ${f.covered}/${f.total} (${f.pct}%) |\n`;
    }
    comment += "\n</details>\n\n";
  }

  // Uncovered lines section
  const filesWithUncovered = fileReports.filter(
    (f) => f.uncoveredLines.length > 0,
  );
  if (filesWithUncovered.length > 0) {
    comment += "### Uncovered lines\n\n";
    for (const file of filesWithUncovered.sort((a, b) =>
      a.path.localeCompare(b.path),
    )) {
      const ranges = consolidateRanges(file.uncoveredLines);
      comment += `- \`${file.path}\`: ${ranges}\n`;
    }
    comment += "\n";
  }

  if (ghOutput) appendFileSync(ghOutput, "has_coverage=true\n");
}

writeFileSync("coverage-comment.md", comment);
console.log(comment);
