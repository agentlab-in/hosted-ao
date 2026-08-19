import { describe, expect, it } from "vitest";
import config from "./forge.config";

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
