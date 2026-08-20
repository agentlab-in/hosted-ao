import { expect, test } from "@playwright/test";
import { installFakeBridge } from "./support/fake-bridge";

// Phase 1 (the pairing string) end to end: paste -> race -> pin.
//
// docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md
// ("Desktop-side flow") is the spec this proves out: pasting an `ao-pair://`
// string parses it (frontend/src/shared/pair-string.ts), races its address(es)
// over the real racing logic (frontend/src/shared/pair-race.ts) through the
// real AddPairedMachineDialog (frontend/src/renderer/components/settings/
// AddPairedMachineDialog.tsx), and auto-pins the winner with no manual
// compare step. Only the `pairedMachines` bridge calls
// (probeFingerprint/add/refresh) are faked, via the opt-in `pairing` fixture
// on installFakeBridge -- see the comment on that option, and on
// `pairedMachines` itself, for why every other spec still gets a hard-failing
// `add()`.
//
// This is renderer smoke, not the packaged-app pod gate: the real gateway TLS
// handshake and certificate pinning (setCertificateVerifyProc, PR #91) are
// exercised there, not here.

const FINGERPRINT = "DF:9A:6C:0D:63:16:53:39:2F:43:4F:02:D8:5F:61:51:63:21:70:BE:21:45:E1:9E:B1:25:D2:44:6F:D4:AB:E5";
const FINGERPRINT_HEX = FINGERPRINT.replace(/:/g, "").toLowerCase();
const HOST = "127.0.0.1";
const PORT = 8443;
const PASSCODE = "abc123XY";
const MACHINE_ID = `paired:${HOST}:${PORT}`;

test("renderer: pasting a pairing string races the address and pins the machine @T0", async ({ page }) => {
	await installFakeBridge(page, {
		pairing: {
			host: HOST,
			port: PORT,
			fingerprint: FINGERPRINT,
			machine: {
				id: MACHINE_ID,
				name: HOST,
				baseUrl: `https://${HOST}:${PORT}`,
				local: false,
				createdAt: null,
				lastSeen: null,
				reachability: "unknown",
				harness: "unknown",
				harnessCommand: null,
			},
		},
	});

	await page.goto("/#/settings");
	await expect(page.getByTestId("settings-page")).toBeVisible();
	// Not paired yet: the machine picker has nothing at this id.
	await expect(page.locator(`[data-testid="ao-machine"][data-machine-id="${MACHINE_ID}"]`)).toHaveCount(0);

	await page.getByRole("button", { name: "Add machine" }).click();
	const dialog = page.getByTestId("pairing-dialog");
	await expect(dialog).toBeVisible();

	await page
		.getByLabel("Pairing string")
		.fill(`ao-pair://v1/${HOST}:${PORT}#${FINGERPRINT_HEX}:${PASSCODE}`);
	await page.getByRole("button", { name: "Continue" }).click();

	// The winning path never shows the visual compare step: the fingerprint is
	// auto-pinned from the pasted string itself, the out-of-band channel the
	// user already trusted.
	await expect(dialog).not.toBeVisible();

	const row = page.locator(`[data-testid="ao-machine"][data-machine-id="${MACHINE_ID}"]`);
	await expect(row).toBeVisible();
	await expect(row).toContainText(HOST);
	await expect(row).toContainText("Paired");
});
