import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { MachOParseError } from "./makers/macho-archs";

// postMake's dmg/zip branches only need to prove they call the right
// maker-dmg functions with the right gates; the functions' own behavior
// (sealDmg's credential matrix, verifyMacArtifact's script invocation) is
// covered by makers/maker-dmg.test.ts. Mocking the whole module keeps this
// suite from spawning codesign/xcrun/bash indirectly through the real chain.
// vi.mock's factory is hoisted above the rest of the file (including plain
// const declarations), so the mock fns themselves must go through vi.hoisted.
const { sealDmg, verifyDmg, verifyMacArtifact, isSigningConfigured } = vi.hoisted(() => ({
	sealDmg: vi.fn<(path: string) => Promise<boolean>>(),
	verifyDmg: vi.fn<(path: string) => Promise<void>>(async () => undefined),
	verifyMacArtifact: vi.fn<(path: string) => Promise<void>>(async () => undefined),
	isSigningConfigured: vi.fn<() => boolean>(),
}));
vi.mock("./makers/maker-dmg", async (importOriginal) => {
	const actual = await importOriginal<typeof import("./makers/maker-dmg")>();
	return { ...actual, sealDmg, verifyDmg, verifyMacArtifact, isSigningConfigured };
});

import config, { extraResourcesForPlatform, macSignOptionsForFile } from "./forge.config";

// Minimal synthetic Mach-O headers (thin little-endian + fat big-endian), the
// two on-disk layouts the signing selector must tell apart. Full parser
// coverage lives in makers/macho-archs.test.ts; here the fixtures exist so the
// per-file signing decision is exercised against real file bytes.
const CPU_TYPE_X86_64 = 0x01000007;
const CPU_TYPE_ARM64 = 0x0100000c;

function thinMachO(cputype: number): Buffer {
	const buffer = Buffer.alloc(16);
	buffer.writeUInt32LE(0xfeedfacf, 0);
	buffer.writeUInt32LE(cputype, 4);
	return buffer;
}

function fatMachO(entries: number[]): Buffer {
	const buffer = Buffer.alloc(8 + entries.length * 20);
	buffer.writeUInt32BE(0xcafebabe, 0);
	buffer.writeUInt32BE(entries.length, 4);
	entries.forEach((cputype, index) => {
		buffer.writeUInt32BE(cputype, 8 + index * 20);
	});
	return buffer;
}

function withHostArch<T>(arch: string, run: () => T): T {
	const descriptor = Object.getOwnPropertyDescriptor(process, "arch");
	Object.defineProperty(process, "arch", { get: () => arch, configurable: true });
	try {
		return run();
	} finally {
		if (descriptor) Object.defineProperty(process, "arch", descriptor);
	}
}

let fixtureDir: string;

// The nested Node must sit at the real bundle path shape: the endsWith gate
// and the content selector are two halves of one decision.
function acpNodeWith(contents: Buffer): string {
	const binDir = join(
		fixtureDir,
		"Agent Orchestrator.app",
		"Contents",
		"Resources",
		"acp-runtime",
		"node",
		"bin",
	);
	mkdirSync(binDir, { recursive: true });
	writeFileSync(join(binDir, "node"), contents);
	return join(binDir, "node");
}

beforeEach(() => {
	fixtureDir = mkdtempSync(join(tmpdir(), "forge-signing-"));
});

afterEach(() => {
	rmSync(fixtureDir, { recursive: true, force: true });
});

describe("native runtime resources", () => {
	it("fails packaging when the macOS helper was not copied into Resources", async () => {
		mkdirSync(join(fixtureDir, "AO.app", "Contents", "Resources"), { recursive: true });
		const hook = config.hooks?.postPackage;
		expect(hook).toBeTypeOf("function");
		if (typeof hook !== "function") return;
		await expect(hook(config, { platform: "darwin", arch: "arm64", outputPaths: [fixtureDir] })).rejects.toThrow("packaged macOS update helper missing");
	});

	it("bundles the native update helper only on macOS", () => {
		expect(extraResourcesForPlatform("darwin")).toContain("update-helper");
		expect(extraResourcesForPlatform("linux")).not.toContain("update-helper");
		expect(extraResourcesForPlatform("win32")).not.toContain("update-helper");
	});

	it.each(["darwin", "linux"] as const)("bundles tmux on %s", (platform) => {
		expect(extraResourcesForPlatform(platform)).toContain("tmux");
	});

	it("does not bundle tmux on Windows", () => {
		expect(extraResourcesForPlatform("win32")).not.toContain("tmux");
	});
});

// Hosted AO pins upstream AO Cloud off permanently (see
// frontend/src/shared/cloud-pin.ts) and must never claim the ao-app://
// scheme: a packaged build that still baked it in would hijack stock
// agent-orchestrator's Cloud sign-in callback on a machine with both
// installed. Build-time claims cannot be runtime-gated, so this asserts
// the claim is absent from every maker's config, not just disabled.
describe("packaged authentication callback registration", () => {
	it("never declares ao-app in the macOS bundle, AppImage, or Linux package metadata", () => {
		expect(config.packagerConfig?.protocols).toBeUndefined();

		const makers = config.makers as Array<{
			name?: string;
			config?: {
				protocols?: unknown;
				options?: { mimeType?: string[] };
			};
		}>;

		const appImageMaker = makers.find((candidate) => candidate.name === "appimage");
		expect(appImageMaker?.config?.protocols).toBeUndefined();

		for (const name of [
			"@electron-forge/maker-deb",
			"@electron-forge/maker-rpm",
		]) {
			const maker = makers.find((candidate) => candidate.name === name);
			expect(maker?.config?.options?.mimeType).toBeUndefined();
		}
	});
});

describe("packaged native dependencies", () => {
	it("keeps the SQLite runtime available to the Vite main bundle", () => {
		const ignore = config.packagerConfig?.ignore;
		expect(ignore).toBeTypeOf("function");
		if (typeof ignore !== "function") return;

		expect(ignore("/.vite/build/main.js")).toBe(false);
		expect(ignore("/node_modules")).toBe(false);
		expect(ignore("/node_modules/better-sqlite3/build/Release/better_sqlite3.node")).toBe(false);
		expect(ignore("/node_modules/bindings/bindings.js")).toBe(false);
		expect(ignore("/node_modules/file-uri-to-path/index.js")).toBe(false);
		expect(ignore("/node_modules/react/index.js")).toBe(true);
		expect(ignore("/src/main.ts")).toBe(true);
		expect(config.hooks?.prePackage).toBeTypeOf("function");
	});
});
