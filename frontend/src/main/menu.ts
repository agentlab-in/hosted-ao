import type { MenuItemConstructorOptions } from "electron";

// Electron's built-in toggleDevTools role assumes the focused surface belongs
// to a BrowserWindow. AO uses BaseWindow with WebContentsView children, so the
// role can receive no focused window and crash the main process. Keep Electron's
// complete standard menus through their top-level roles, but replace View so
// DevTools routes through AO's guarded handler.
export function buildWindowsAppMenuTemplate(
  onToggleDevTools?: () => void,
): MenuItemConstructorOptions[] {
  const devtoolsItem: MenuItemConstructorOptions = {
    label: "Toggle DevTools",
    accelerator: "Ctrl+Shift+I",
    // An explicit click handler, not Electron's built-in { role: "toggleDevTools" },
    // so this stays consistent with the guarded mac template below and can never
    // dispatch through Electron's internal (unguarded) role handling.
    click: () => onToggleDevTools?.(),
  };
  return [
    {
      label: "Edit",
      submenu: [
        { role: "undo" },
        { role: "redo" },
        { type: "separator" },
        { role: "cut" },
        { role: "copy" },
        { role: "paste" },
        { role: "selectAll" },
      ],
    },
    {
      label: "View",
      submenu: [
        { role: "reload" },
        devtoolsItem,
        { type: "separator" },
        { role: "resetZoom" },
        { accelerator: "Ctrl+=", role: "zoomIn" },
        {
          accelerator: "Ctrl+Plus",
          acceleratorWorksWhenHidden: true,
          role: "zoomIn",
          visible: false,
        },
        { accelerator: "Ctrl+-", role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    {
      label: "Window",
      submenu: [{ role: "minimize" }, { role: "close" }],
    },
  ];
}

// macOS never gets an explicit application menu installed elsewhere in main.ts,
// so without this Electron falls back to its own default menu. That default's
// View > Toggle Developer Tools uses the built-in { role: "toggleDevTools" },
// which reads the focused window's webContents inside Electron's own menu
// dispatch and throws before any app code runs once no window is focused
// (agentlab-in/hosted-ao#115) — the app stays running with no focused window
// whenever every window is closed on macOS. This template mirrors Electron's
// default darwin menu (electron/lib/browser/default-menu.ts) item-for-item,
// swapping only that one entry for an explicit, no-op-safe click handler.
export function buildMacAppMenuTemplate(
  onToggleDevTools?: () => void,
): MenuItemConstructorOptions[] {
  const devtoolsItem: MenuItemConstructorOptions = {
    label: "Toggle Developer Tools",
    // Keeping the accelerator explicit matters: dropping the role also drops
    // its default binding, and this is the same one Electron's role uses.
    accelerator: "Alt+Command+I",
    click: () => onToggleDevTools?.(),
  };
  return [
    { role: "appMenu" },
    { role: "fileMenu" },
    { role: "editMenu" },
    {
      label: "View",
      submenu: [
        { role: "reload" },
        { role: "forceReload" },
        devtoolsItem,
        { type: "separator" },
        { role: "resetZoom" },
        { role: "zoomIn" },
        { role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    { role: "windowMenu" },
    { role: "help", submenu: [] },
  ];
}
