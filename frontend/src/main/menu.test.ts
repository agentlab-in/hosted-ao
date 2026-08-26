import { describe, expect, it, vi } from "vitest";
import { buildMacAppMenuTemplate, buildWindowsAppMenuTemplate } from "./menu";

type MenuItem = ReturnType<typeof buildWindowsAppMenuTemplate>[number];
type SubmenuItem = NonNullable<Extract<MenuItem["submenu"], readonly unknown[]>>[number];

function viewSubmenu(): readonly SubmenuItem[] {
	const viewMenu = buildWindowsAppMenuTemplate().find((item) => item.label === "View");
	if (!viewMenu || !Array.isArray(viewMenu.submenu)) {
		throw new Error("View menu not found");
	}
	return viewMenu.submenu;
}

function macViewSubmenu(onToggleDevTools?: () => void): readonly SubmenuItem[] {
	const viewMenu = buildMacAppMenuTemplate(onToggleDevTools).find((item) => item.label === "View");
	if (!viewMenu || !Array.isArray(viewMenu.submenu)) {
		throw new Error("View menu not found");
	}
	return viewMenu.submenu;
}

describe("buildWindowsAppMenuTemplate", () => {
	it("registers both plus key forms for zoom in", () => {
		const zoomInItems = viewSubmenu().filter((item) => item.role === "zoomIn");

		expect(zoomInItems).toEqual(
			expect.arrayContaining([
				expect.objectContaining({ accelerator: "Ctrl+=", role: "zoomIn" }),
				expect.objectContaining({ accelerator: "Ctrl+Plus", role: "zoomIn", visible: false }),
			]),
		);
	});

	it("keeps the direct minus accelerator for zoom out", () => {
		expect(viewSubmenu()).toContainEqual(expect.objectContaining({ accelerator: "Ctrl+-", role: "zoomOut" }));
	});

	it("never uses the built-in toggleDevTools role, since it throws when no window is focused", () => {
		const devtoolsItem = viewSubmenu().find((item) => item.label === "Toggle DevTools");
		expect(devtoolsItem).toBeDefined();
		expect(devtoolsItem?.role).toBeUndefined();
		expect(typeof devtoolsItem?.click).toBe("function");
	});

	it("no-ops the devtools click handler when no callback is supplied", () => {
		const devtoolsItem = viewSubmenu().find((item) => item.label === "Toggle DevTools");
		expect(() => (devtoolsItem?.click as (...args: unknown[]) => void)?.()).not.toThrow();
	});

	it("invokes the supplied callback when the devtools item is clicked", () => {
		const onToggleDevTools = vi.fn();
		const viewMenu = buildWindowsAppMenuTemplate(onToggleDevTools).find((item) => item.label === "View");
		const devtoolsItem = (viewMenu?.submenu as SubmenuItem[] | undefined)?.find(
			(item) => item.label === "Toggle DevTools",
		);
		(devtoolsItem?.click as (...args: unknown[]) => void)?.();
		expect(onToggleDevTools).toHaveBeenCalledOnce();
	});
});

describe("buildMacAppMenuTemplate", () => {
	it("installs an explicit application menu instead of leaving Electron's default in place", () => {
		const template = buildMacAppMenuTemplate();
		expect(template.map((item) => item.role ?? item.label)).toEqual([
			"appMenu",
			"fileMenu",
			"editMenu",
			"View",
			"windowMenu",
			"help",
		]);
	});

	it("never uses the built-in toggleDevTools role, since it throws when no window is focused", () => {
		const devtoolsItem = macViewSubmenu().find((item) => item.label === "Toggle Developer Tools");
		expect(devtoolsItem).toBeDefined();
		expect(devtoolsItem?.role).toBeUndefined();
		expect(typeof devtoolsItem?.click).toBe("function");
	});

	it("keeps the default devtools accelerator so dropping the role doesn't drop the binding", () => {
		const devtoolsItem = macViewSubmenu().find((item) => item.label === "Toggle Developer Tools");
		expect(devtoolsItem?.accelerator).toBe("Alt+Command+I");
	});

	it("no-ops the devtools click handler when no callback is supplied", () => {
		const devtoolsItem = macViewSubmenu().find((item) => item.label === "Toggle Developer Tools");
		expect(() => (devtoolsItem?.click as (...args: unknown[]) => void)?.()).not.toThrow();
	});

	it("invokes the supplied callback when the devtools item is clicked", () => {
		const onToggleDevTools = vi.fn();
		const devtoolsItem = macViewSubmenu(onToggleDevTools).find((item) => item.label === "Toggle Developer Tools");
		(devtoolsItem?.click as (...args: unknown[]) => void)?.();
		expect(onToggleDevTools).toHaveBeenCalledOnce();
	});

	it("keeps the rest of the View menu on Electron's own roles", () => {
		expect(macViewSubmenu().map((item) => item.role ?? item.type)).toEqual([
			"reload",
			"forceReload",
			undefined,
			"separator",
			"resetZoom",
			"zoomIn",
			"zoomOut",
			"separator",
			"togglefullscreen",
		]);
	});
});

describe("buildMacAppMenuTemplate", () => {
	function macViewSubmenu(onToggleDevTools = () => undefined): readonly SubmenuItem[] {
		const viewMenu = buildMacAppMenuTemplate(onToggleDevTools).find(
			(item) => item.label === "View",
		);
		if (!viewMenu || !Array.isArray(viewMenu.submenu)) {
			throw new Error("View menu not found");
		}
		return viewMenu.submenu;
	}

	it("uses a guarded click handler instead of Electron's crashing DevTools role", () => {
		const onToggleDevTools = vi.fn();
		const devtoolsItems = macViewSubmenu(onToggleDevTools).filter(
			(item) => item.label === "Toggle Developer Tools" || item.role === "toggleDevTools",
		);

		expect(devtoolsItems).toHaveLength(1);
		expect(devtoolsItems[0]).toMatchObject({
			accelerator: "Alt+Command+I",
			label: "Toggle Developer Tools",
		});
		expect(devtoolsItems[0].role).toBeUndefined();

		devtoolsItems[0].click?.(undefined as never, undefined as never, undefined as never);
		expect(onToggleDevTools).toHaveBeenCalledOnce();
	});

	it("preserves Electron's complete standard macOS menus", () => {
		const template = buildMacAppMenuTemplate(() => undefined);

		expect(template.map((item) => item.role)).toEqual(
			expect.arrayContaining(["appMenu", "fileMenu", "editMenu", "windowMenu"]),
		);
		expect(macViewSubmenu()).toContainEqual(expect.objectContaining({ role: "forceReload" }));
	});
});
