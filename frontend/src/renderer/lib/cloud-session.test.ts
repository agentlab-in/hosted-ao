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

// The pin must come from the single shared constant (../../shared/cloud-pin),
// not a local copy of the boolean. If cloud-session.ts ever reverted to its
// own hardcoded literal, mocking the shared module would stop affecting this
// module's behavior and the assertion below would fail.
describe("isCloudSignInConfigured reads the shared cloud-pin constant", () => {
  afterEach(() => {
    vi.doUnmock("../../shared/cloud-pin");
    vi.resetModules();
  });

  it("would unhide Cloud sign-in if the shared pin were flipped on", async () => {
    vi.doMock("../../shared/cloud-pin", () => ({
      CLOUD_SIGN_IN_ENABLED: true,
    }));
    vi.resetModules();
    const { isCloudSignInConfigured: reloaded } = await import(
      "./cloud-session"
    );
    expect(reloaded("client_123")).toBe(true);
  });
});
