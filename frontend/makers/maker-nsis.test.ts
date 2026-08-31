import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Capture buildForge's args without pulling in electron-builder's real machinery.
const buildForge = vi.fn<(forge: { dir: string }, options: any) => Promise<string[]>>(async () => [
	"/out/make/Agent Orchestrator Setup.exe",
]);
vi.mock("app-builder-lib", () => ({ buildForge }));

import MakerNSIS from "./maker-nsis";

const makeOptions = {
	dir: "/tmp/app/Agent Orchestrator-win32-x64",
	makeDir: "/tmp/app/make",
	appName: "Agent Orchestrator",
	targetPlatform: "win32" as const,
	targetArch: "x64" as const,
	forgeConfig: {} as never,
	packageJSON: {},
};

describe("MakerNSIS", () => {
	it("targets win32 and is supported anywhere (cross-build allowed)", () => {
		const maker = new MakerNSIS();
		expect(maker.name).toBe("nsis");
		expect(maker.defaultPlatforms).toEqual(["win32"]);
		expect(maker.isSupportedOnCurrentPlatform()).toBe(true);
	});

	it("builds an nsis target for the requested arch and forwards config", async () => {
		const maker = new MakerNSIS({ appId: "dev.agent-orchestrator.desktop", icon: "assets/icon.ico" }, ["win32"]);
		// Forge resolves the (possibly arch-dependent) config before make().
		await maker.prepareConfig(makeOptions.targetArch);
		const artifacts = await maker.make(makeOptions);

		expect(artifacts).toEqual(["/out/make/Agent Orchestrator Setup.exe"]);
		const [forgeOptions, options] = buildForge.mock.calls[0];
		expect(forgeOptions).toEqual({ dir: makeOptions.dir });
		expect(options.win).toEqual(["nsis:x64"]);
		// electron-builder must not try to publish; the workflow does that.
		expect(options.config.publish).toBeNull();
		expect(options.config.appId).toBe("dev.agent-orchestrator.desktop");
		// productName falls back to appName when not set on the maker config.
		expect(options.config.productName).toBe("Agent Orchestrator");
		expect(options.config.win).toEqual({ icon: "assets/icon.ico" });
		// A real installer: not Squirrel's silent one-click per-user drop.
		expect(options.config.nsis.oneClick).toBe(false);
		expect(options.config.nsis.allowToChangeInstallationDirectory).toBe(true);
	});

	it("forwards executableName so the Start menu shortcut targets the real binary (#2414)", async () => {
		const maker = new MakerNSIS(
			{ appId: "dev.agent-orchestrator.desktop", executableName: "agent-orchestrator", icon: "assets/icon.ico" },
			["win32"],
		);
		await maker.prepareConfig(makeOptions.targetArch);
		await maker.make(makeOptions);

		const [, options] = buildForge.mock.calls.at(-1)!;
		// electron-builder derives the exe name — and thus the shortcut's TargetPath
		// and icon — from win.executableName, falling back to productName otherwise.
		// It must match Forge's packaged "agent-orchestrator.exe", not the
		// "Agent Orchestrator.exe" it would infer from productName.
		expect(options.config.win.executableName).toBe("agent-orchestrator");
		expect(options.config.win.icon).toBe("assets/icon.ico");
	});
});

describe("MakerNSIS unsigned-only policy", () => {
	const signingEnvKeys = [
		"WIN_CSC_LINK",
		"WIN_CSC_KEY_PASSWORD",
		"WIN_CERT_SUBJECT_NAME",
		"WIN_SIGNING_HASH_ALGORITHMS",
		"AZURE_PUBLISHER_NAME",
		"AZURE_TENANT_ID",
		"AZURE_CLIENT_ID",
		"AZURE_CLIENT_SECRET",
		"AZURE_SUBSCRIPTION_ID",
		"AZURE_RESOURCE_GROUP_NAME",
		"AZURE_ACCOUNT_NAME",
		"AZURE_CODE_SIGNING_ACCOUNT_NAME",
		// CSC_LINK/CSC_KEY_PASSWORD must never be consumed for Windows; kept
		// here so every test starts from a clean slate even on a machine that
		// exports the macOS credentials.
		"CSC_LINK",
		"CSC_KEY_PASSWORD",
	] as const;

	const savedEnv = new Map<string, string | undefined>();

	beforeEach(() => {
		for (const key of signingEnvKeys) {
			savedEnv.set(key, process.env[key]);
			delete process.env[key];
		}
	});

	afterEach(() => {
		for (const [key, value] of savedEnv) {
			if (value === undefined) delete process.env[key];
			else process.env[key] = value;
		}
	});

	async function makeWithNoConfig() {
		const maker = new MakerNSIS({}, ["win32"]);
		await maker.prepareConfig(makeOptions.targetArch);
		await maker.make(makeOptions);
		return buildForge.mock.calls.at(-1)![1];
	}

	it("stays unsigned and unforced when no signing credentials are set", async () => {
		const options = await makeWithNoConfig();
		expect(options.config.win).toBeUndefined();
	});

	it("ignores the macOS CSC_LINK secret — a stray Apple .p12 must not wedge the Windows build", async () => {
		// Regression guard: CSC_LINK is the Apple Developer ID .p12. Signing
		// (and thus forceCodeSigning) must only activate via WIN_CSC_LINK.
		process.env.CSC_LINK = "/keychain/AppleDeveloperID.p12";
		process.env.CSC_KEY_PASSWORD = "apple-secret";
		const options = await makeWithNoConfig();
		expect(options.config.win).toBeUndefined();
	});

	it("ignores every supported Windows signing credential path", async () => {
		process.env.WIN_CSC_LINK = "C:\\certs\\windows-codesign.pfx";
		process.env.WIN_CSC_KEY_PASSWORD = "win-secret";
		process.env.WIN_SIGNING_HASH_ALGORITHMS = "sha256, sha1";
		process.env.WIN_CERT_SUBJECT_NAME = "Contoso Code Signing EV";
		process.env.AZURE_PUBLISHER_NAME = "Contoso";
		process.env.AZURE_TENANT_ID = "tenant-1";
		process.env.AZURE_CLIENT_ID = "client-1";
		process.env.AZURE_CLIENT_SECRET = "secret-1";
		process.env.AZURE_ACCOUNT_NAME = "ao-signing";
		const options = await makeWithNoConfig();
		expect(options.config.win).toBeUndefined();
	});
});
