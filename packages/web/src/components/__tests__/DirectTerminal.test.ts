import { describe, it, expect } from "vitest";
import { buildDirectTerminalWsUrl, buildTerminalThemes } from "@/components/DirectTerminal";

describe("buildDirectTerminalWsUrl", () => {
  it("keeps non-standard port when proxy path override is set", () => {
    const wsUrl = buildDirectTerminalWsUrl({
      location: {
        protocol: "https:",
        hostname: "example.com",
        host: "example.com:8443",
        port: "8443",
      },
      sessionId: "session-1",
      proxyWsPath: "/ao-terminal-ws",
    });

    expect(wsUrl).toBe("wss://example.com:8443/ao-terminal-ws?session=session-1");
  });

  it("uses proxy path without port when default port is used", () => {
    const wsUrl = buildDirectTerminalWsUrl({
      location: {
        protocol: "https:",
        hostname: "example.com",
        host: "example.com",
        port: "",
      },
      sessionId: "session-2",
      proxyWsPath: "/ao-terminal-ws",
    });

    expect(wsUrl).toBe("wss://example.com/ao-terminal-ws?session=session-2");
  });

  it("uses default path-based endpoint on standard ports when no proxy override is set", () => {
    const wsUrl = buildDirectTerminalWsUrl({
      location: {
        protocol: "https:",
        hostname: "example.com",
        host: "example.com",
        port: "443",
      },
      sessionId: "session-3",
    });

    expect(wsUrl).toBe("wss://example.com/ao-terminal-ws?session=session-3");
  });

  it("uses direct terminal port on non-standard ports when no proxy override is set", () => {
    const wsUrl = buildDirectTerminalWsUrl({
      location: {
        protocol: "http:",
        hostname: "localhost",
        host: "localhost:3000",
        port: "3000",
      },
      sessionId: "session-4",
      directTerminalPort: "14888",
    });

    expect(wsUrl).toBe("ws://localhost:14888/ws?session=session-4");
  });
});

const HEX_RE = /^#[0-9a-fA-F]{6}$/;
const ANSI_KEYS = [
  "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite",
] as const;

describe("buildTerminalThemes", () => {
  it("dark theme has valid hex colors for bg, fg, and all ANSI slots", () => {
    const { dark } = buildTerminalThemes("agent");
    expect(dark.background).toMatch(HEX_RE);
    expect(dark.foreground).toMatch(HEX_RE);
    for (const key of ANSI_KEYS) {
      expect(dark[key]).toMatch(HEX_RE);
    }
  });

  it("light theme has valid hex colors for bg, fg, and all ANSI slots", () => {
    const { light } = buildTerminalThemes("agent");
    expect(light.background).toBe("#fafafa");
    expect(light.foreground).toBe("#383a42");
    for (const key of ANSI_KEYS) {
      expect(light[key]).toMatch(HEX_RE);
    }
  });

  it("dark theme background is #0a0a0f", () => {
    const { dark } = buildTerminalThemes("agent");
    expect(dark.background).toBe("#0a0a0f");
  });

  it("variant changes cursor color between agent and orchestrator", () => {
    const agent = buildTerminalThemes("agent");
    const orch = buildTerminalThemes("orchestrator");
    expect(agent.dark.cursor).not.toBe(orch.dark.cursor);
    expect(agent.light.cursor).not.toBe(orch.light.cursor);
  });

  it("selection colors differ between dark and light themes", () => {
    const { dark, light } = buildTerminalThemes("agent");
    expect(dark.selectionBackground).not.toBe(light.selectionBackground);
  });
});
