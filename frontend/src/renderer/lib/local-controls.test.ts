import { afterEach, beforeEach, expect, it, vi } from "vitest";
const { token } = vi.hoisted(() => ({ token: vi.fn(async () => "paired-secret") }));
vi.mock("./bridge", () => ({ aoBridge: { machines: { gatewayToken: token } } }));
vi.mock("./telemetry", () => ({ captureRendererEvent: vi.fn(async () => {}) }));
vi.mock("./sentry", () => ({ captureApiErrorToSentry: vi.fn() }));
import { apiClient, setApiBaseUrl } from "./api-client";

beforeEach(() => {
  token.mockClear();
  setApiBaseUrl("https://machine.example.com");
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{}", { status: 200 }));
});
afterEach(() => { vi.restoreAllMocks(); setApiBaseUrl("http://127.0.0.1:3001"); });

it("rejects remote credential reads and login commands before bearer acquisition", async () => {
  const accounts = await apiClient.GET("/api/v1/agents/codex/accounts");
  const login = await apiClient.POST("/api/v1/agents/codex/accounts/login-terminal");
  expect(accounts.response.status).toBe(403);
  expect(login.response.status).toBe(403);
  expect(accounts.error).toMatchObject({ code: "local_machine_required" });
  expect(token).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});

it("rejects remote harness and system installer mutations", async () => {
  const harness = await apiClient.POST("/api/v1/agents/{agent}/install", {
    params: { path: { agent: "codex" } }, body: { method: "npm", operation: "install" },
  });
  const system = await apiClient.POST("/api/v1/system/install/{target}", {
    params: { path: { target: "cloudflared" } },
  });
  expect(harness.response.status).toBe(403);
  expect(system.response.status).toBe(403);
  expect(fetch).not.toHaveBeenCalled();
});

it("blocks system installer reads while keeping ordinary remote REST authenticated", async () => {
  const result = await apiClient.GET("/api/v1/system/install/{target}", { params: { path: { target: "cloudflared" } } });
  expect(result.response.status).toBe(403);
  expect(fetch).not.toHaveBeenCalled();
  expect(token).not.toHaveBeenCalled();
  await apiClient.GET("/api/v1/projects");
  expect(fetch).toHaveBeenCalledOnce();
  expect(token).toHaveBeenCalledOnce();
  const init = vi.mocked(fetch).mock.calls[0][1];
  expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer paired-secret");
});

it("allows credential queries on the selected local daemon without a gateway bearer", async () => {
  setApiBaseUrl("http://127.0.0.1:3001");
  const result = await apiClient.GET("/api/v1/agents/codex/accounts");
  expect(result.response.status).toBe(200);
  expect(fetch).toHaveBeenCalledOnce();
  expect(token).not.toHaveBeenCalled();
});


it("keeps launch readiness local because it can refresh account credentials", async () => {
  const result = await apiClient.POST("/api/v1/agents/readiness/ensure", {
    body: { agentIds: [], purpose: "launch" },
  });
  expect(result.response.status).toBe(403);
  expect(token).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
  await apiClient.GET("/api/v1/agents/readiness");
  expect(fetch).toHaveBeenCalledOnce();
  expect(token).toHaveBeenCalledOnce();
});


it("preserves local launch readiness without a remote bearer", async () => {
  setApiBaseUrl("http://127.0.0.1:3001");
  const result = await apiClient.POST("/api/v1/agents/readiness/ensure", {
    body: { agentIds: [], purpose: "launch" },
  });
  expect(result.response.status).toBe(200);
  expect(fetch).toHaveBeenCalledOnce();
  expect(token).not.toHaveBeenCalled();
  const init = vi.mocked(fetch).mock.calls[0][1];
  expect(new Headers(init?.headers).has("Authorization")).toBe(false);
});
