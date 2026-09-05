import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";
import { apiClient } from "../lib/api-client";

// vi.mock is hoisted above module-level consts, so the shared double has to be
// created inside vi.hoisted to exist by the time the factory runs.
const { mobileStatus } = vi.hoisted(() => ({
	mobileStatus: {
		enabled: true,
		host: "192.168.1.42",
		tailscaleHost: "100.72.46.7",
		port: 3011,
		password: "fake-password-for-testing",
		// Retain the upstream status shape while the producer emits authenticated v1.
		hostId: "h_fixture",
		endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
		tunnel: undefined as
			| undefined
			| {
					supported: boolean;
					running: boolean;
					ready: boolean;
					hostname: string;
					location: string;
					lastError: string;
			  },
		warning: "",
		securePairing: {
			enabled: false,
			available: false,
			active: false,
			host: "",
			port: 0,
			reason: "",
		},
	},
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("../lib/api-client", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/api-client")>()),
	apiClient: {
		GET: async (path: string) =>
			path === "/api/v1/mobile/devices"
				? { data: { devices: [] }, error: undefined }
				: { data: mobileStatus, error: undefined },
		POST: vi.fn(async () => ({ data: {}, error: undefined })),
	},
	apiErrorMessage: () => "failed",
}));

import {
	ConnectMobileContent,
	mobileStatusRefetchInterval,
	pairingPayload,
} from "./settings/ConnectMobileContent";

function renderMobileSettings() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<ConnectMobileContent active />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

// Read the payload off the wrapper used by the shared styled QR component.
function qrPayload(): string | null {
	const qr = document.querySelector("[data-qr-value]");
	return qr?.getAttribute("data-qr-value") ?? null;
}

// Mirrors what the phone does with a scanned code.
function decodeQr(value: string): Record<string, unknown> {
	return JSON.parse(value);
}

async function selectConnectionMethod(mode: "LAN" | "Tailscale") {
	await userEvent.click(await screen.findByRole("button", { name: "Connection method" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: mode }));
}

beforeEach(() => {
	mobileStatus.enabled = true;
	mobileStatus.host = "192.168.1.42";
	mobileStatus.hostId = "h_fixture";
	mobileStatus.tunnel = undefined;
	mobileStatus.endpoints = [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }];
	mobileStatus.tailscaleHost = "100.72.46.7";
	mobileStatus.warning = "";
	mobileStatus.securePairing = {
		enabled: false,
		available: false,
		active: false,
		host: "",
		port: 0,
		reason: "",
	};
});

test("QR payload carries host, port, and password for one-scan connect", () => {
	const s = pairingPayload("192.168.1.42", 3011, "fake-password-for-testing");
	expect(JSON.parse(s)).toEqual({ v: 1, host: "192.168.1.42", port: 3011, password: "fake-password-for-testing" });
});

test("encodes the LAN address by default", async () => {
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(decodeQr(qrPayload()!)).toMatchObject({ v: 1, host: "192.168.1.42", port: 3011, password: mobileStatus.password });
});

test("can turn off the generated mobile connection", async () => {
	renderMobileSettings();
	const button = await screen.findByRole("button", { name: "Turn off mobile connection" });

	await userEvent.click(button);
	expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/mobile/disable");
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it : the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("keeps the connection-method dropdown at its fixed width", async () => {
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	const control = screen.getByRole("button", { name: "Connection method" });
	expect(control).toHaveClass("w-44", "justify-between");

	await userEvent.click(control);
	expect(screen.getByRole("menu")).toHaveClass("!w-44", "!min-w-0");
});

// The trusted home-network scope does not require Wi-Fi specifically.
test("states the network scope without requiring Wi-Fi", async () => {
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	expect(screen.queryByText(/same Wi-Fi/i)).not.toBeInTheDocument();
	expect(screen.getByText("Generate and scan the QR from the AO app")).toBeInTheDocument();
});

// Both stores are listed in step one now. The platform dropdown that used to
// gate them is gone, so neither link may be behind an interaction.
test("offers both store links without a platform choice", async () => {
	renderMobileSettings();

	expect(await screen.findByRole("button", { name: "Open Agent Orchestrator on the App Store" })).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Open Agent Orchestrator on Google Play" })).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Get the app" })).not.toBeInTheDocument();
});

test("shows a square Google Play QR tooltip for Android", async () => {
	renderMobileSettings();
	await userEvent.hover(await screen.findByRole("button", { name: "Open Agent Orchestrator on Google Play" }));

	expect(await screen.findByTestId("android-play-qr")).toHaveClass("p-2");
});

test("shows a QR-only App Store tooltip", async () => {
	renderMobileSettings();
	await userEvent.hover(await screen.findByRole("button", { name: "Open Agent Orchestrator on the App Store" }));

	const tooltip = await screen.findByTestId("ios-store-qr");
	expect(tooltip).not.toHaveTextContent("App Store");
	expect(tooltip.querySelector("svg")).toBeInTheDocument();
});

// The connection picker is unavailable; re-enable this scenario with it.
test.skip("encodes the address selected by the connection mode", async () => {
	mobileStatus.endpoints = [
		{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
		{ kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false },
	];
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	const before = qrPayload()!;

	await selectConnectionMethod("Tailscale");
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	expect(decodeQr(before).host).toBe("192.168.1.42");
	expect(decodeQr(qrPayload()!).host).toBe("100.72.46.7");
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it : the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("shows a hint instead of a QR when Tailscale is not running", async () => {
	mobileStatus.tailscaleHost = "";
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(screen.getByText(/Tailscale isn't running/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// Regression: an empty host used to encode {"v":1,"host":"",...}, which the
// phone rejects as "not an AO pairing code" : an incoherent error for a QR AO
// generated itself.
test("shows a hint instead of an unscannable QR when there is no LAN address", async () => {
	mobileStatus.host = "";
	renderMobileSettings();
	await waitFor(() => expect(screen.getByText(/No network address found/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it : the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("the address line follows the selected mode", async () => {
	renderMobileSettings();
	const address = await screen.findByTestId("mobile-pairing-address");
	expect(within(address).getByText("192.168.1.42:3011")).toBeInTheDocument();

	await selectConnectionMethod("Tailscale");
	await waitFor(() => expect(within(address).getByText("100.72.46.7:3011")).toBeInTheDocument());
});

test("omits the secure key entirely for plaintext pairing", () => {
	expect(JSON.parse(pairingPayload("192.168.1.42", 3011, "pw"))).toEqual({
		v: 1, host: "192.168.1.42", port: 3011, password: "pw",
	});
});

test("encodes secure:true when secure pairing is active", () => {
	expect(JSON.parse(pairingPayload("host.tail1.ts.net", 443, "pw", true))).toEqual({
		v: 1, host: "host.tail1.ts.net", port: 443, password: "pw", secure: true,
	});
});

// The connection picker is unavailable; retain its TLS contract for restoration.
test.skip("carries the secure-pairing MagicDNS host in the v1 code", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: true, active: true,
		host: "prasads-macbook-pro.tail057d04.ts.net", port: 443, reason: "",
	};
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(decodeQr(qrPayload()!)).toMatchObject({ host: "prasads-macbook-pro.tail057d04.ts.net", port: 443, secure: true });
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it : the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("shows setup steps and no QR when certs are not enabled", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: false, active: false,
		host: "h.tail1.ts.net", port: 0, reason: "no_certs",
	};
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(screen.getByText(/HTTPS certificates/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// A failing secure-pairing POST must surface an error rather than silently
// snapping the switch back on the next status refetch with no explanation.
test("surfaces an error when enabling secure pairing fails", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: true, active: false,
		host: "", port: 0, reason: "",
	};
	const { apiClient } = await import("../lib/api-client");
	vi.mocked(apiClient.POST).mockImplementationOnce(async () => ({
		data: undefined,
		error: { message: "secure pairing failed" },
	}));
	renderMobileSettings();

	await waitFor(() => expect(screen.getByText("failed")).toBeInTheDocument());
});

// TLS is no longer a switch: iOS refuses cleartext to a 100.x address, so a
// Tailscale pairing with it off works on Android and fails on iPhone with
// nothing to explain why. Selecting Tailscale turns it on.
test("turns secure pairing on wherever a tailnet address exists", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: true, active: false,
		host: "", port: 0, reason: "",
	};
	const { apiClient } = await import("../lib/api-client");
	renderMobileSettings();

	await waitFor(() =>
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/mobile/secure-pairing", { body: { enabled: true } }),
	);
	expect(screen.queryByRole("switch", { name: "Secure pairing (TLS)" })).not.toBeInTheDocument();
	// Nothing to report while it works: the panel guarantees TLS, so only a
	// failure earns space.
	expect(screen.queryByTestId("secure-pairing-reason")).not.toBeInTheDocument();
});

// A tailnet with no certificates rejects every attempt. Retrying on each status
// poll would hammer the daemon; the reason text is what tells the user.
test("does not retry enabling secure pairing when it is unavailable", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: false, active: false,
		host: "", port: 0, reason: "no_certs",
	};
	const { apiClient } = await import("../lib/api-client");
	// POST is a suite-wide mock; a previous test's secure-pairing call would
	// otherwise satisfy the negative assertion below.
	vi.mocked(apiClient.POST).mockClear();
	renderMobileSettings();

	await waitFor(() => expect(screen.getByTestId("secure-pairing-reason")).toBeInTheDocument());
	expect(apiClient.POST).not.toHaveBeenCalledWith("/api/v1/mobile/secure-pairing", { body: { enabled: true } });
});


// The rendered producer must match the retained phone v1 parser.
test("emits authenticated v1 even when upstream endpoints are absent", async () => {
	mobileStatus.endpoints = [];
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(decodeQr(qrPayload()!)).toEqual({
		v: 1, host: "192.168.1.42", port: 3011, password: mobileStatus.password,
	});
	expect(qrPayload()).not.toContain("aomobile://");
});

test("does not poll for a public connector", () => {
	expect(mobileStatusRefetchInterval({ tunnel: { running: true, ready: false } })).toBe(false);
});

test("polls only until an enabled listener advertises endpoints", () => {
	expect(mobileStatusRefetchInterval({ enabled: true, endpoints: [] })).toBeGreaterThan(0);
	expect(mobileStatusRefetchInterval({ enabled: true, endpoints: [
		{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
	] })).toBe(false);
});

test("stops polling once the tunnel is advertisable", () => {
	expect(mobileStatusRefetchInterval({ tunnel: { running: true, ready: true } })).toBe(false);
});

test("does not poll when there is no tunnel to wait for", () => {
	expect(mobileStatusRefetchInterval({ tunnel: { running: false, ready: false } })).toBe(false);
	expect(mobileStatusRefetchInterval({ tunnel: undefined })).toBe(false);
	expect(mobileStatusRefetchInterval(undefined)).toBe(false);
});

// Remote access is optional: nothing installs cloudflared, so on a machine
// without it there is no connector at all. A zero tunnel status made that
// indistinguishable from "not started yet", so the QR looked entirely normal
// and the user discovered the gap only by being away from home.
test("says so when this machine cannot be reached from elsewhere", async () => {
	mobileStatus.tunnel = {
		supported: false, running: false, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	expect(await screen.findByTestId("mobile-remote-unavailable")).toHaveTextContent(
		/only|cloudflared/i,
	);
});

// Legacy connector status does not change the Hosted network boundary.
test("states the home-network boundary regardless of legacy connector status", async () => {
	mobileStatus.tunnel = {
		supported: true, running: true, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	expect(await screen.findByTestId("mobile-remote-unavailable")).toHaveTextContent("home network only");
});

// Hosted AO retains authenticated LAN pairing without a production connector installer.
test("keeps LAN pairing available without a cloudflared installer", async () => {
	mobileStatus.tunnel = {
		supported: false, running: false, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	await screen.findByTestId("mobile-remote-unavailable");
	expect(screen.queryByTestId("mobile-install-cloudflared")).toBeNull();
});

test("does not offer an install when a connector already exists", async () => {
	mobileStatus.tunnel = {
		supported: true, running: true, ready: true, hostname: "x.trycloudflare.com", location: "", lastError: "",
	};
	renderMobileSettings();

	await waitFor(() => expect(screen.queryByTestId("mobile-install-cloudflared")).toBeNull());
});
