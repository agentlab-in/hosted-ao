import { afterEach, describe, expect, it, vi } from "vitest";
import { isCloudSignInConfigured } from "./cloud-session";

// Hosted AO pins upstream AO Cloud off permanently; the Cloud sign-in entry
// must never render here regardless of a WorkOS client ID being present.
describe("isCloudSignInConfigured", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("hides Cloud sign-in when the WorkOS client ID is absent", () => {
    expect(isCloudSignInConfigured(undefined)).toBe(false);
    expect(isCloudSignInConfigured("   ")).toBe(false);
  });

  it("still hides Cloud sign-in when a WorkOS client ID is configured", () => {
    expect(isCloudSignInConfigured("client_123")).toBe(false);
    vi.stubEnv("VITE_WORKOS_CLIENT_ID", "client_123");
    expect(isCloudSignInConfigured()).toBe(false);
  });
});
