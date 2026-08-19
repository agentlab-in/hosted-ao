import type { AoBridge } from "../../preload";
import type { AoAccountState } from "../../shared/ao-account";
import {
	HARNESS_SETUP_COMMAND,
	LOCAL_MACHINE_ID,
	localMachine,
	type AoMachine,
	type AoMachinesState,
} from "../../shared/ao-machines";
import { DEFAULT_CONTROL_PLANE_URL } from "../../shared/control-plane";
import { machineDaemonStatus } from "../../shared/remote-daemon";
import type { DaemonStatus } from "../../shared/daemon-status";
import type { PeerProject, PeerWorkspacesResult } from "../../shared/peer-workspaces";
import { coerceLocale } from "../../shared/ui-locale";
export type { FeatureBuild } from "../../main/feature-builds";

const BROWSER_PREVIEW_ACCOUNT_STATE: AoAccountState = {
	status: "unavailable",
	controlPlaneUrl: DEFAULT_CONTROL_PLANE_URL,
	error: "Signing in needs the desktop app; it is not available in browser preview.",
};

// Browser preview has no main process, so the machine list cannot be fetched.
// These stand in for it, and they are also what `ao preview` renders when the
// machine picker is being reviewed: this computer, a registered machine with no
// agent harness, and a machine that is down.
const previewRemoteMachine = (machine: Partial<AoMachine> & Pick<AoMachine, "id" | "name" | "baseUrl">): AoMachine => ({
	local: false,
	createdAt: null,
	lastSeen: null,
	reachability: "online",
	harness: "unknown",
	harnessCommand: null,
	...machine,
});

const BROWSER_PREVIEW_MACHINES: AoMachine[] = [
	localMachine("This Mac"),
	previewRemoteMachine({
		id: "mch_ready",
		name: "ao-build-01",
		baseUrl: "https://vm.example.com",
		harness: "ready",
	}),
	previewRemoteMachine({
		id: "mch_no_harness",
		name: "ao-scratch",
		baseUrl: "https://scratch.example.com",
		harness: "missing",
		harnessCommand: HARNESS_SETUP_COMMAND,
	}),
	previewRemoteMachine({
		id: "mch_offline",
		name: "ao-eu-west",
		baseUrl: "https://eu-west.example.com",
		reachability: "offline",
		lastSeen: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
	}),
];

let previewActiveMachineId = LOCAL_MACHINE_ID;

const browserPreviewMachinesState = (): AoMachinesState => ({
	status: "ready",
	machines: BROWSER_PREVIEW_MACHINES,
	activeMachineId: previewActiveMachineId,
});

// A couple of demoable sessions for the "Turn on Cloud" preview, whichever
// side of the toggle plays the peer.
const BROWSER_PREVIEW_PEER_PROJECTS: PeerProject[] = [
	{
		id: "proj_peer",
		name: "reverbcode",
		sessions: [
			{
				id: "peer_s1",
				title: "Fix the login redirect loop",
				status: "working",
				activity: "active",
				branch: "ao/dev/fix-login-redirect",
				harness: "codex",
				kind: "worker",
				updatedAt: new Date().toISOString(),
			},
			{
				id: "peer_s2",
				title: "Add dark mode toggle",
				status: "pr_open",
				activity: "idle",
				branch: "ao/dev/dark-mode-toggle",
				harness: "claude-code",
				kind: "worker",
				updatedAt: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
			},
		],
	},
];

/** Peer = the daemon that is NOT active, mirroring machines.select's target. */
const browserPreviewPeerWorkspaces = (): PeerWorkspacesResult => {
	if (previewActiveMachineId === LOCAL_MACHINE_ID) {
		const peer = BROWSER_PREVIEW_MACHINES.find((machine) => machine.id === "mch_ready");
		if (!peer) return { state: "unavailable", reason: "No cloud machine registered." };
		return {
			state: "ok",
			machineId: peer.id,
			machineName: peer.name,
			isRemote: true,
			projects: BROWSER_PREVIEW_PEER_PROJECTS,
		};
	}
	return {
		state: "ok",
		machineId: LOCAL_MACHINE_ID,
		machineName: "This Mac",
		isRemote: false,
		projects: BROWSER_PREVIEW_PEER_PROJECTS,
	};
};

/**
 * What the app's daemon status would be for the machine picked in preview, from
 * the same function the main process uses. Picking a registered machine here
 * therefore shows the real not-ready state rather than a preview-only stand-in.
 */
const browserPreviewDaemonStatus = (): DaemonStatus => {
	const active = BROWSER_PREVIEW_MACHINES.find((machine) => machine.id === previewActiveMachineId);
	if (!active || active.local) {
		return { state: "stopped", message: "Electron preload is not available in browser preview." };
	}
	return machineDaemonStatus(active);
};

const previewDaemonStatusListeners = new Set<(status: DaemonStatus) => void>();

export const aoBridge: AoBridge =
	window.ao ??
	({
		app: {
			getVersion: async () => "0.0.0-preview",
			chooseDirectory: async () => null,
			openExternal: async (url: string) => {
				window.open(url, "_blank", "noopener,noreferrer");
			},
			scanImportFolder: async ({ path }) => ({ path, repos: [] }),
			checkAncestorRepo: async () => undefined,
			onNewSessionShortcut: () => () => undefined,
			onKeyboardShortcutsHelp: () => () => undefined,
			onNewShellTerminalShortcut: () => () => undefined,
			onCloseShellTerminalShortcut: () => () => undefined,
			setCloseShellTerminalShortcutEnabled: () => undefined,
			onOpenSettingsShortcut: () => () => undefined,
			onPreviousSessionShortcut: () => () => undefined,
			onNextSessionShortcut: () => () => undefined,
			onPreviousTabShortcut: () => () => undefined,
			onNextTabShortcut: () => () => undefined,
			onFocusTerminalShortcut: () => () => undefined,
		},
		terminal: {
			saveDroppedFile: async () => "",
			setFocused: () => undefined,
			onFontSizeShortcut: () => () => undefined,
		},
		window: {
			isMaximized: async () => false,
			onMaximized: () => () => undefined,
			isFullScreen: async () => false,
			onFullScreen: () => () => undefined,
		},
		theme: {
			set: async () => undefined,
		},
		menu: {
			action: async () => undefined,
			notifyShellFocus: () => undefined,
		},
		clipboard: {
			writeText: async (text: string) => {
				if (navigator.clipboard?.writeText) {
					await navigator.clipboard.writeText(text);
				}
			},
			readText: async () => (navigator.clipboard?.readText ? navigator.clipboard.readText() : ""),
		},
		daemon: {
			getStatus: async () => browserPreviewDaemonStatus(),
			start: async () => ({ state: "starting" }),
			stop: async () => ({ state: "stopped" }),
			restart: async () => ({ state: "starting" }),
			onStatus: (listener: (status: DaemonStatus) => void) => {
				previewDaemonStatusListeners.add(listener);
				return () => previewDaemonStatusListeners.delete(listener);
			},
		},
		telemetry: {
			getBootstrap: async () => null,
		},
		browser: {
			nativeCompositionEnabled: false,
			ensure: async (sessionId: string) => ({
				viewId: `preview:${sessionId}`,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			setBounds: () => undefined,
			setOverlayOpen: () => undefined,
			navigate: async ({ viewId, url }) => ({
				viewId,
				url,
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			clear: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			goBack: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			goForward: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			reload: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			stop: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			getTabs: async (viewId: string) => ({ viewId, activeTabId: "t1", tabs: [] }),
			selectTab: async ({ viewId, tabId }) => ({ viewId, activeTabId: tabId, tabs: [] }),
			closeTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			openTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			devtools: async ({ viewId, operation }) => ({
				viewId,
				open: operation !== "close",
				activeTabId: "",
			}),
			destroy: () => undefined,
			setAnnotationMode: async () => undefined,
			onNavState: () => () => undefined,
			onTabsState: () => () => undefined,
			onAgentActivity: () => () => undefined,
			onDevToolsState: () => () => undefined,
			onAnnotationSubmit: () => () => undefined,
			onAnnotationCancel: () => () => undefined,
		},
		notifications: {
			show: async () => undefined,
			setBadge: async () => undefined,
			devBounce: async () => undefined,
			onClick: () => () => undefined,
		},
		tray: {
			setAttentionState: () => undefined,
			onOpenSession: () => () => undefined,
		},
		appState: {
			getMigration: async () => ({ status: "pending" }),
			setMigration: async () => undefined,
		},
		updateSettings: {
			get: async () => ({ enabled: false, channel: "latest", nightlyAck: false, feature: null }),
			set: async () => undefined,
		},
		uiSettings: {
			get: async () => ({ locale: "en" as const }),
			set: async (settings) => ({ locale: coerceLocale(settings.locale) }),
		},
		keybindings: {
			get: async () => ({}),
			set: async (overrides) => overrides,
			setRecording: async () => undefined,
		},
		updates: {
			getStatus: async () => ({ state: "idle" }),
			check: async () => undefined,
			returnHome: async () => undefined,
			download: async () => undefined,
			install: async () => undefined,
			onStatus: () => () => undefined,
			onTelemetry: () => () => undefined,
		},
		featureBuilds: {
			list: async () => [],
			getActive: async () => null,
		},
		// Browser preview has no main process, so no keychain and no loopback listener.
		// Report it as unavailable rather than offering a button that cannot work.
		account: {
			getState: async () => BROWSER_PREVIEW_ACCOUNT_STATE,
			signIn: async () => BROWSER_PREVIEW_ACCOUNT_STATE,
			signOut: async () => BROWSER_PREVIEW_ACCOUNT_STATE,
		},
		machines: {
			getState: async () => browserPreviewMachinesState(),
			refresh: async () => browserPreviewMachinesState(),
			select: async (machineId: string) => {
				previewActiveMachineId = machineId;
				const status = browserPreviewDaemonStatus();
				previewDaemonStatusListeners.forEach((listener) => listener(status));
				return browserPreviewMachinesState();
			},
			// Browser preview has no main process, so no machine token either.
			// Null means "send no Authorization header".
			gatewayToken: async () => null,
			peerWorkspaces: async () => browserPreviewPeerWorkspaces(),
		},
		// Browser preview has no main process, so no pin store and nothing to
		// probe or pair.
		pairedMachines: {
			list: async () => [],
			refresh: async () => [],
			probeFingerprint: async () => ({
				error: "Pairing needs the desktop app; it is not available in browser preview.",
			}),
			getPinnedFingerprint: async () => null,
			add: async () => {
				throw new Error("Pairing needs the desktop app; it is not available in browser preview.");
			},
			remove: async () => undefined,
		},
		cloud: {
			getSession: async () => null,
			signIn: async () => undefined,
			signOut: async () => undefined,
			onSessionChanged: () => () => undefined,
		},
	} satisfies AoBridge);
