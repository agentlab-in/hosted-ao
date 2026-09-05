import { describe, expect, it } from "vitest";
import { resolveRuntimePaths } from "./state-root";
import { defaultRunFilePath } from "./daemon-discovery";
import { resolveDaemonLaunch } from "./daemon-launch";
import { defaultDataDir } from "./telemetry";
import { resolveDesktopDataDir } from "../main/telemetry-policy-file";

const home = "/home/ao";
const cwd = "/launch/work";
const cases = [
	{ name: "default", env: {}, root: "/home/ao/.ao/hosted", data: "/home/ao/.ao/hosted/data", run: "/home/ao/.ao/hosted/running.json" },
	{ name: "run only", env: { AO_RUN_FILE: "../state/custom.json" }, root: "/launch/state", data: "/launch/state/data", run: "/launch/state/custom.json" },
	{ name: "data only", env: { AO_DATA_DIR: "../state/custom-db" }, root: "/launch/state", data: "/launch/state/custom-db", run: "/launch/state/running.json" },
	{ name: "both independent", env: { AO_RUN_FILE: "/run/state/../custom.json", AO_DATA_DIR: "/durable/custom-db" }, root: "/run", data: "/durable/custom-db", run: "/run/custom.json" },
	{ name: "trimmed", env: { AO_RUN_FILE: "  ../state/run.json  ", AO_DATA_DIR: "  ./store  " }, root: "/launch/state", data: "/launch/work/store", run: "/launch/state/run.json" },
	{ name: "blank overrides", env: { AO_RUN_FILE: " ", AO_DATA_DIR: "\t", APPDATA: "/os/app-data", XDG_CONFIG_HOME: "/os/config" }, root: "/home/ao/.ao/hosted", data: "/home/ao/.ao/hosted/data", run: "/home/ao/.ao/hosted/running.json" },
];

describe("canonical desktop runtime paths", () => {
	it.each(cases)("shares the Go root contract for $name", ({ env, root, data, run }) => {
		expect(resolveRuntimePaths(env, home, cwd, "linux")).toEqual({ stateRoot: root, dataDir: data, runFile: run, userData: `${root}/electron` });
		expect(defaultRunFilePath("linux", env, home, cwd)).toBe(run);
		expect(defaultDataDir("linux", env, home, cwd)).toBe(data);
		for (const packaged of [true, false]) {
			const defaultDev = !packaged && !env.AO_RUN_FILE?.trim() && !env.AO_DATA_DIR?.trim();
			expect(resolveDesktopDataDir(env, home, cwd, packaged)).toBe(defaultDev ? `${home}/.ao/hosted/dev/data` : data);
		}
		expect(resolveDaemonLaunch(env, true, "/resources", "/app", home, "linux", cwd)?.cwd).toBe(root);
	});
	it("normalizes Windows overrides before switching drive or cwd", () => {
		const paths = resolveRuntimePaths({ AO_RUN_FILE: String.raw`..\runtime\run.json`, AO_DATA_DIR: String.raw`D:\durable\store` }, String.raw`C:\Users\ao`, String.raw`C:\launch\work`, "win32");
		expect(paths).toEqual({ stateRoot: String.raw`C:\launch\runtime`, runFile: String.raw`C:\launch\runtime\run.json`, dataDir: String.raw`D:\durable\store`, userData: String.raw`C:\launch\runtime\electron` });
	});
	it("does not require a home when an explicit override supplies the root", () => {
		expect(resolveRuntimePaths({ AO_DATA_DIR: "store" }, "", cwd, "linux").stateRoot).toBe(cwd);
		expect(defaultRunFilePath("linux", { AO_RUN_FILE: "run.json" }, "", cwd)).toBe(`${cwd}/run.json`);
	});
	it("fails instead of falling back to OS app-data when no root is available", () => {
		expect(() => resolveRuntimePaths({ APPDATA: "/app-data", XDG_CONFIG_HOME: "/config" }, "", cwd, "linux")).toThrow("home directory");
	});
	it("does not reinterpret a tilde override as the user home", () => {
		expect(resolveRuntimePaths({ AO_DATA_DIR: "~/data" }, home, cwd, "linux").dataDir).toBe(`${cwd}/~/data`);
	});
});

 describe("development and invalid state paths", () => {
 it("keeps default development state isolated", () => {
 expect(resolveRuntimePaths({}, home, cwd, "linux", true)).toEqual({stateRoot: `${home}/.ao/hosted/dev`, dataDir: `${home}/.ao/hosted/dev/data`, runFile: `${home}/.ao/hosted/dev/running.json`, userData: `${home}/.ao/hosted/dev/electron`});
 });
 it.each(["AO_RUN_FILE", "AO_DATA_DIR"])("rejects NUL in %s without echoing the path", (key) => {
 expect(() => resolveRuntimePaths({[key]: "private\0path"}, home, cwd)).toThrow(/^AO state path contains NUL$/);
 });
 });
