"use client";

import { useEffect, useRef, useState, useMemo } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { useTheme } from "next-themes";
import { cn } from "@/lib/cn";
import { useMux } from "@/hooks/useMux";

// Import xterm CSS (must be imported in client component)
import "xterm/css/xterm.css";

// Dynamically import xterm types for TypeScript
import type { ITheme, Terminal as TerminalType } from "xterm";
import type { FitAddon as FitAddonType } from "@xterm/addon-fit";

interface DirectTerminalProps {
  sessionId: string;
  startFullscreen?: boolean;
  /** Visual variant. Orchestrator keeps the same design-system blue accent as the rest of the app. */
  variant?: "agent" | "orchestrator";
  appearance?: "theme" | "dark";
  /** CSS height for the terminal container in normal (non-fullscreen) mode.
   *  Defaults to "max(440px, calc(100vh - 440px))". */
  height?: string;
  isOpenCodeSession?: boolean;
  reloadCommand?: string;
  chromeless?: boolean;
}

type TerminalVariant = "agent" | "orchestrator";


export function buildTerminalThemes(variant: TerminalVariant): { dark: ITheme; light: ITheme } {
  const agentAccent = {
    cursor: "#5b7ef8",
    selDark: "rgba(91, 126, 248, 0.30)",
    selLight: "rgba(91, 126, 248, 0.25)",
  };
  const orchAccent = agentAccent;
  const accent = variant === "orchestrator" ? orchAccent : agentAccent;

  const dark: ITheme = {
    background: "#0a0a0f",
    foreground: "#d4d4d8",
    cursor: accent.cursor,
    cursorAccent: "#0a0a0f",
    selectionBackground: accent.selDark,
    selectionInactiveBackground: "rgba(128, 128, 128, 0.2)",
    // ANSI colors — slightly warmer than pure defaults
    black: "#1a1a24",
    red: "#ef4444",
    green: "#22c55e",
    yellow: "#f59e0b",
    blue: "#5b7ef8",
    magenta: "#a371f7",
    cyan: "#22d3ee",
    white: "#d4d4d8",
    brightBlack: "#50506a",
    brightRed: "#f87171",
    brightGreen: "#4ade80",
    brightYellow: "#fbbf24",
    brightBlue: "#7b9cfb",
    brightMagenta: "#c084fc",
    brightCyan: "#67e8f9",
    brightWhite: "#eeeef5",
  };

  const light: ITheme = {
    background: "#fafafa",
    foreground: "#24292f",
    cursor: accent.cursor,
    cursorAccent: "#fafafa",
    selectionBackground: accent.selLight,
    selectionInactiveBackground: "rgba(128, 128, 128, 0.15)",
    // ANSI colors — darkened for legibility on white
    black: "#1f2937",
    red: "#dc2626",
    green: "#16a34a",
    yellow: "#ca8a04",
    blue: "#2563eb",
    magenta: "#9333ea",
    cyan: "#0891b2",
    white: "#6b7280",
    brightBlack: "#4b5563",
    brightRed: "#ef4444",
    brightGreen: "#22c55e",
    brightYellow: "#eab308",
    brightBlue: "#3b82f6",
    brightMagenta: "#a855f7",
    brightCyan: "#06b6d4",
    brightWhite: "#9ca3af",
  };

  return { dark, light };
}

export function DirectTerminal({
  sessionId,
  startFullscreen = false,
  variant = "agent",
  appearance = "theme",
  height = "max(440px, calc(100vh - 440px))",
  isOpenCodeSession = false,
  reloadCommand = "/exit",
  chromeless = false,
}: DirectTerminalProps) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const { resolvedTheme } = useTheme();
  const { subscribeTerminal, writeTerminal, openTerminal, closeTerminal, resizeTerminal, status: muxStatus } = useMux();

  const terminalRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<TerminalType | null>(null);
  const fitAddonRef = useRef<FitAddonType | null>(null);
  const cleanupRef = useRef<(() => void) | null>(null);
  const resizeObserverRef = useRef<ResizeObserver | null>(null);
  const [connected, setConnected] = useState(false);
  const [fullscreen, setFullscreen] = useState(startFullscreen);
  const [reloading, setReloading] = useState(false);
  const [reloadError, setReloadError] = useState<string | null>(null);

  const terminalThemes = useMemo(() => buildTerminalThemes(variant), [variant]);
  const isDark = appearance === "dark" || (appearance === "theme" && resolvedTheme !== "light");
  const theme = isDark ? terminalThemes.dark : terminalThemes.light;

  const updateFullscreenQuery = (next: boolean) => {
    const params = new URLSearchParams(searchParams?.toString() ?? "");
    if (next) {
      params.set("fullscreen", sessionId);
    } else if (params.get("fullscreen") === sessionId) {
      params.delete("fullscreen");
    }
    const query = params.toString();
    router.replace(query ? `${pathname}?${query}` : pathname, { scroll: false });
  };

  useEffect(() => {
    const requestedSession = searchParams?.get("fullscreen");
    if (requestedSession === sessionId && !fullscreen) {
      setFullscreen(true);
    } else if (requestedSession !== sessionId && fullscreen && startFullscreen === false) {
      setFullscreen(false);
    }
  }, [fullscreen, searchParams, sessionId, startFullscreen]);

  useEffect(() => {
    let cancelled = false;

    async function init() {
      if (!terminalRef.current || termRef.current) return;

      const [{ Terminal }, { FitAddon }, { WebLinksAddon }] = await Promise.all([
        import("xterm"),
        import("@xterm/addon-fit"),
        import("@xterm/addon-web-links"),
      ]);

      if (cancelled || !terminalRef.current) return;

      const term = new Terminal({
        theme,
        convertEol: true,
        cursorBlink: true,
        allowTransparency: true,
        fontFamily:
          "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', 'Courier New', monospace",
        fontSize: 13,
        lineHeight: 1.25,
        scrollback: 5000,
      });
      const fitAddon = new FitAddon();
      const webLinksAddon = new WebLinksAddon();

      termRef.current = term;
      fitAddonRef.current = fitAddon;
      term.loadAddon(fitAddon);
      term.loadAddon(webLinksAddon);
      term.open(terminalRef.current);
      fitAddon.fit();

      // Keep xterm selection styling in sync on theme changes
      term.options.theme = theme;

      // Allow copy from selected text with Cmd/Ctrl+C without sending interrupt
      term.attachCustomKeyEventHandler((e) => {
        const isCopy = (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "c";
        if (isCopy && term.hasSelection()) {
          navigator.clipboard?.writeText(term.getSelection());
          return false;
        }
        return true;
      });

      const onDataDispose = term.onData((data) => {
        writeTerminal(sessionId, data);
      });

      cleanupRef.current = subscribeTerminal(sessionId, (event) => {
        if (event.type === "data") {
          term.write(event.data);
          setConnected(true);
        } else if (event.type === "open") {
          setConnected(true);
        } else if (event.type === "error") {
          setReloadError(event.message);
        }
      });

      openTerminal(sessionId);

      resizeObserverRef.current = new ResizeObserver(() => {
        fitAddon.fit();
        if (term.cols && term.rows) {
          resizeTerminal(sessionId, term.cols, term.rows);
        }
      });
      resizeObserverRef.current.observe(terminalRef.current);

      const fontsReady = (document as Document & { fonts?: FontFaceSet }).fonts?.ready;
      fontsReady?.then(() => {
        fitAddon.fit();
        if (term.cols && term.rows) {
          resizeTerminal(sessionId, term.cols, term.rows);
        }
      });

      return () => {
        onDataDispose.dispose();
      };
    }

    const disposePromise = init();
    return () => {
      cancelled = true;
      disposePromise?.then((dispose) => dispose?.());
      resizeObserverRef.current?.disconnect();
      resizeObserverRef.current = null;
      cleanupRef.current?.();
      cleanupRef.current = null;
      closeTerminal(sessionId);
      termRef.current?.dispose();
      termRef.current = null;
      fitAddonRef.current = null;
      setConnected(false);
    };
  }, [closeTerminal, openTerminal, resizeTerminal, sessionId, subscribeTerminal, theme, writeTerminal]);

  useEffect(() => {
    if (!termRef.current || !fitAddonRef.current) return;
    termRef.current.options.theme = theme;
    fitAddonRef.current.fit();
    if (termRef.current.cols && termRef.current.rows) {
      resizeTerminal(sessionId, termRef.current.cols, termRef.current.rows);
    }
  }, [resizeTerminal, sessionId, theme]);

  useEffect(() => {
    if (!fitAddonRef.current || !termRef.current) return;
    const id = window.setTimeout(() => {
      fitAddonRef.current?.fit();
      if (termRef.current?.cols && termRef.current?.rows) {
        resizeTerminal(sessionId, termRef.current.cols, termRef.current.rows);
      }
    }, 16);
    return () => window.clearTimeout(id);
  }, [fullscreen, resizeTerminal, sessionId]);

  const handleToggleFullscreen = () => {
    const next = !fullscreen;
    setFullscreen(next);
    updateFullscreenQuery(next);
  };

  const handleReload = async () => {
    if (reloading || muxStatus !== "connected") return;
    setReloading(true);
    setReloadError(null);
    try {
      writeTerminal(sessionId, `${reloadCommand}`);
    } catch (error) {
      setReloadError(error instanceof Error ? error.message : "Failed to restart session");
    } finally {
      setReloading(false);
    }
  };

  const fullscreenButton = (
    <button
      onClick={handleToggleFullscreen}
      className="inline-flex items-center gap-1 rounded-[6px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-elevated)] px-2 py-1 text-[11px] font-medium text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-subtle)] hover:text-[var(--color-text-primary)]"
      aria-label={fullscreen ? "exit fullscreen" : "fullscreen"}
      title={fullscreen ? "Exit fullscreen" : "Fullscreen"}
    >
      {fullscreen ? (
        <svg className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
          <path d="M8 3H3v5" />
          <path d="M16 3h5v5" />
          <path d="M8 21H3v-5" />
          <path d="M16 21h5v-5" />
        </svg>
      ) : (
        <svg className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
          <path d="M8 3H3v5" />
          <path d="M16 3h5v5" />
          <path d="M8 21H3v-5" />
          <path d="M16 21h5v-5" />
        </svg>
      )}
      {fullscreen ? "Exit" : "Fullscreen"}
    </button>
  );

  return (
    <div
      className={cn(
        "overflow-hidden border border-[var(--color-border)] bg-[var(--color-bg-panel)] shadow-[var(--shadow-sm)]",
        fullscreen
          ? "fixed inset-0 z-50 rounded-none border-0"
          : "relative rounded-[12px]",
      )}
      style={{
        ...(fullscreen ? { width: "100vw", height: "100dvh" } : {}),
      }}
    >
      {!chromeless ? (
        <div className="flex h-[37px] items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-bg-elevated)] px-3">
          <div className="flex min-w-0 items-center gap-2">
            <span className="text-[12px] font-semibold uppercase tracking-[0.08em] text-[var(--color-accent)]">
              XDA
            </span>
            <span className="truncate text-[12px] text-[var(--color-text-secondary)]">
              {sessionId}
            </span>
            <span
              className={cn(
                "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium",
                connected
                  ? "bg-[color-mix(in_srgb,var(--color-accent)_18%,transparent)] text-[var(--color-accent)]"
                  : "bg-[var(--color-bg-subtle)] text-[var(--color-text-tertiary)]",
              )}
            >
              {connected ? "Connected" : "Connecting"}
            </span>
          </div>
          <div className="flex items-center gap-2">
            {isOpenCodeSession ? (
              <button
                onClick={handleReload}
                disabled={reloading || muxStatus !== "connected"}
                title="Restart OpenCode session (/exit then resume mapped session)"
                aria-label="Restart OpenCode session"
                className="inline-flex items-center gap-1 rounded-[6px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-elevated)] px-2 py-1 text-[11px] font-medium text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-subtle)] hover:text-[var(--color-text-primary)] disabled:cursor-not-allowed disabled:opacity-70"
              >
                {reloading ? (
                  <>
                    <svg
                      className="h-3 w-3 animate-spin"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="2"
                      viewBox="0 0 24 24"
                    >
                      <path d="M12 3a9 9 0 109 9" />
                    </svg>
                    restarting
                  </>
                ) : (
                  <>
                    <svg
                      className="h-3 w-3"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="2"
                      viewBox="0 0 24 24"
                    >
                      <path d="M21 12a9 9 0 11-2.64-6.36" />
                      <path d="M21 3v6h-6" />
                    </svg>
                    restart
                  </>
                )}
              </button>
            ) : null}
            {reloadError ? (
              <span
                className="max-w-[220px] truncate text-[10px] font-medium text-[var(--color-status-error)]"
                title={reloadError}
              >
                {reloadError}
              </span>
            ) : null}
            {fullscreenButton}
          </div>
        </div>
      ) : null}
      {chromeless ? (
        <div className="absolute right-3 top-3 z-10 flex items-center gap-1 rounded-[6px] border border-[var(--color-border-subtle)] bg-[color-mix(in_srgb,var(--color-bg-elevated)_92%,transparent)] px-1.5 py-1 shadow-[0_8px_24px_rgba(0,0,0,0.18)] backdrop-blur-sm">
          {isOpenCodeSession ? (
            <button
              onClick={handleReload}
              disabled={reloading || muxStatus !== "connected"}
              title="Restart OpenCode session (/exit then resume mapped session)"
              aria-label="Restart OpenCode session"
              className="flex items-center gap-1 px-2 py-0.5 text-[11px] text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-subtle)] hover:text-[var(--color-text-primary)] disabled:cursor-not-allowed disabled:opacity-70"
            >
              {reloading ? (
                <>
                  <svg
                    className="h-3 w-3 animate-spin"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    viewBox="0 0 24 24"
                  >
                    <path d="M12 3a9 9 0 109 9" />
                  </svg>
                  restarting
                </>
              ) : (
                <>
                  <svg
                    className="h-3 w-3"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    viewBox="0 0 24 24"
                  >
                    <path d="M21 12a9 9 0 11-2.64-6.36" />
                    <path d="M21 3v6h-6" />
                  </svg>
                  restart
                </>
              )}
            </button>
          ) : null}
          {fullscreenButton}
        </div>
      ) : null}
      {/* Terminal area */}
      <div
        ref={terminalRef}
        className={cn("w-full p-1.5")}
        style={{
          overflow: "hidden",
          display: "flex",
          flexDirection: "column",
          height: fullscreen ? `calc(100dvh - ${chromeless ? "0px" : "37px"})` : height,
        }}
      />
    </div>
  );
}
