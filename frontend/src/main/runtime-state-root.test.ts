import { readFileSync } from "node:fs";
import path from "node:path";
import vm from "node:vm";
import ts from "typescript";
import { describe, expect, it, vi } from "vitest";
import { resolveRuntimePaths } from "../shared/state-root";
import { buildDaemonEnv, devDaemonAllowedOrigins } from "../shared/shell-env";

// Execute the actual bootstrap/path functions without loading Electron or starting the app.
const source = ts.createSourceFile("main.ts", readFileSync(path.resolve(__dirname, "../main.ts"), "utf8"), ts.ScriptTarget.Latest, true);
const names = new Set(["desktopLaunchWorkingDirectory", "desktopPaths", "desktopDataDir"]);
const functions = new Set(["daemonEnv", "runFilePath", "resolvedDaemonDataDir", "cloudDataDir"]);
const selected = source.statements.filter((statement) => {
	if (ts.isVariableStatement(statement)) return statement.declarationList.declarations.some((d) => ts.isIdentifier(d.name) && names.has(d.name.text));
	if (ts.isFunctionDeclaration(statement)) return !!statement.name && functions.has(statement.name.text);
	return ts.isExpressionStatement(statement) && statement.getText(source).startsWith('app.setPath("userData",');
}).map((s) => s.getText(source)).join("\n");
const script = ts.transpileModule(`${selected}\n({ daemonEnv, runFilePath, resolvedDaemonDataDir, cloudDataDir });`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText;

describe("Electron and daemon root wiring", () => {
	it.each([{packaged: true, override: true}, {packaged: false, override: true}, {packaged: true, override: false}, {packaged: false, override: false}])("pins userData and child paths before ready, $packaged/$override", ({packaged, override}) => {
		const configured = { AO_RUN_FILE: "../runtime/custom.json", AO_DATA_DIR: "../durable/custom-db", AO_ACP_RUNTIME_DIR: "/installed/acp" };
		const env = override ? configured : { AO_ACP_RUNTIME_DIR: "/installed/acp" };
		const root = override ? "/launch/runtime" : `/home/ao/.ao/hosted${packaged ? "" : "/dev"}`;
		const data = override ? "/launch/durable/custom-db" : `${root}/data`;
		const run = override ? `${root}/custom.json` : `${root}/running.json`;
		const setPath = vi.fn();
		let cwd = "/launch/work";
		const wired = vm.runInNewContext(script, {
			process: { env, cwd: () => cwd, platform: "linux", resourcesPath: "/resources" },
			os: { homedir: () => "/home/ao" }, path,
			app: { isPackaged: packaged, setPath, getAppPath: () => "/app" },
			resolveRuntimePaths, isDev: !packaged, keepDaemonAlive: () => false,
			stagedBundledTmuxBinary: null, appRunId: "test", DEV_DAEMON_PORT: 3002,
			cachedShellEnv: { AO_RUN_FILE: "/wrong/run.json", AO_DATA_DIR: "/wrong/data" },
			telemetryOverrides: () => ({}), rendererUrl: () => "http://localhost:5173", buildDaemonEnv, devDaemonAllowedOrigins,
		}) as { daemonEnv(): NodeJS.ProcessEnv; runFilePath(): string; resolvedDaemonDataDir(): string; cloudDataDir(): string };
		cwd = "/later/child/cwd";
		expect(setPath).toHaveBeenCalledWith("userData", `${root}/electron`);
		expect(wired.runFilePath()).toBe(run);
		expect(wired.resolvedDaemonDataDir()).toBe(data);
		expect(wired.cloudDataDir()).toBe(root);
		expect(wired.daemonEnv()).toMatchObject({ AO_RUN_FILE: run, AO_DATA_DIR: data, AO_ACP_RUNTIME_DIR: "/installed/acp" });
	});
});
