import { describe, expect, it, vi } from "vitest";
import type { ControlPlaneTokenSource } from "./ao-control-token";
import {
	createMachineTokenSource,
	MachineUnavailableError,
	MACHINE_UNAVAILABLE_MESSAGE,
	SIGN_IN_REJECTED_MESSAGE,
	type MachineTokenSourceDeps,
} from "./ao-machine-token";

const CONTROL_PLANE_URL = "https://ao.agentlab.in";
const CONTROL_TOKEN = "control.plane.jwt";
const MACHINE_TOKEN = "machine.audience.jwt";

const controlToken = (token: string | null = CONTROL_TOKEN): ControlPlaneTokenSource => ({
	get: vi.fn().mockResolvedValue(token),
	clear: vi.fn(),
});

const okBody = (extra: Record<string, unknown> = {}) =>
	new Response(JSON.stringify({ access_token: MACHINE_TOKEN, token_type: "Bearer", expires_in: 900, ...extra }), {
		status: 200,
		headers: { "Content-Type": "application/json" },
	});

const errorBody = (status: number, body: Record<string, unknown>) =>
	new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function source(overrides: Partial<MachineTokenSourceDeps> = {}, clock = { ms: 1_000_000 }) {
	// A fresh Response per call: a body can only be read once.
	const fetchImpl = overrides.fetchImpl ?? vi.fn(async () => okBody());
	return {
		clock,
		fetchImpl: fetchImpl as ReturnType<typeof vi.fn>,
		tokens: createMachineTokenSource({
			controlPlaneUrl: CONTROL_PLANE_URL,
			machineId: "mch_1",
			controlToken: controlToken(),
			fetchImpl,
			now: () => clock.ms,
			...overrides,
		}),
	};
}

describe("createMachineTokenSource", () => {
	it("mints at POST /api/v1/machines/{id}/token with the control-plane bearer and no body", async () => {
		const { tokens, fetchImpl, clock } = source();

		await expect(tokens.get()).resolves.toEqual({ token: MACHINE_TOKEN, expiresAt: clock.ms + 900_000 });
		expect(fetchImpl).toHaveBeenCalledTimes(1);
		const [url, init] = fetchImpl.mock.calls[0];
		expect(url).toBe(`${CONTROL_PLANE_URL}/api/v1/machines/mch_1/token`);
		expect(init).toMatchObject({
			method: "POST",
			headers: { Authorization: `Bearer ${CONTROL_TOKEN}`, Accept: "application/json" },
		});
		// The refresh token goes to POST /api/v1/token and nowhere else, and this
		// endpoint takes no request body at all.
		expect(init.body).toBeUndefined();
	});

	it("percent-encodes the machine id rather than pasting it into the path", async () => {
		const { tokens, fetchImpl } = source({ machineId: "mch/../other" });

		await tokens.get();

		expect(fetchImpl.mock.calls[0][0]).toBe(`${CONTROL_PLANE_URL}/api/v1/machines/mch%2F..%2Fother/token`);
	});

	it("drives expiry off the returned expires_in, not a hardcoded fifteen minutes", async () => {
		const { tokens, clock } = source({ fetchImpl: vi.fn(async () => okBody({ expires_in: 1800 })) });

		await expect(tokens.get()).resolves.toMatchObject({ expiresAt: clock.ms + 1_800_000 });
	});

	it("falls back to the contract's default TTL when expires_in is absent or unusable", async () => {
		for (const expiresIn of [undefined, 0, -60, "900"]) {
			const { tokens, clock } = source({ fetchImpl: vi.fn(async () => okBody({ expires_in: expiresIn })) });
			await expect(tokens.get()).resolves.toMatchObject({ expiresAt: clock.ms + 15 * 60_000 });
		}
	});

	it("serves the cached token until the skew window, then mints again", async () => {
		const clock = { ms: 1_000_000 };
		const { tokens, fetchImpl } = source({}, clock);

		await tokens.get();
		clock.ms += 900_000 - 61_000; // still more than the skew away from expiry
		await tokens.get();
		expect(fetchImpl).toHaveBeenCalledTimes(1);

		clock.ms += 2_000; // inside the skew window
		await tokens.get();
		// Nothing rotates on this endpoint, so re-minting is simply repeating the
		// call; no persist-before-use ordering is involved.
		expect(fetchImpl).toHaveBeenCalledTimes(2);
	});

	it("shares one mint between concurrent callers", async () => {
		const { tokens, fetchImpl } = source();

		const [a, b, c] = await Promise.all([tokens.get(), tokens.get(), tokens.get()]);

		expect(fetchImpl).toHaveBeenCalledTimes(1);
		expect(a).toEqual(b);
		expect(b).toEqual(c);
	});

	it("mints again after clear(), so a machine switch cannot reuse the old audience", async () => {
		const { tokens, fetchImpl } = source();

		await tokens.get();
		tokens.clear();
		await tokens.get();

		expect(fetchImpl).toHaveBeenCalledTimes(2);
	});

	it("returns null without calling the control plane when this install is signed out", async () => {
		const fetchImpl = vi.fn();
		const { tokens } = source({ controlToken: controlToken(null), fetchImpl });

		await expect(tokens.get()).resolves.toBeNull();
		expect(fetchImpl).not.toHaveBeenCalled();
	});

	// The control plane answers a revoked machine, another account's machine, and
	// an unknown id identically, so nothing here may infer which it was.
	it("turns the deliberately ambiguous 404 into MachineUnavailableError", async () => {
		const fetchImpl = vi
			.fn()
			.mockResolvedValue(errorBody(404, { error: "not_found", error_description: "no such machine" }));
		const { tokens } = source({ fetchImpl });

		await expect(tokens.get()).rejects.toThrow(MachineUnavailableError);
		await expect(tokens.get()).rejects.toThrow(MACHINE_UNAVAILABLE_MESSAGE);
	});

	it("says to sign in again when the control plane refuses the control-plane token", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(errorBody(401, { error: "invalid_token" }));
		const { tokens } = source({ fetchImpl });

		await expect(tokens.get()).rejects.toThrow(SIGN_IN_REJECTED_MESSAGE);
	});

	it("reports the status for any other failure", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(errorBody(500, { error: "server_error" }));
		const { tokens } = source({ fetchImpl });

		await expect(tokens.get()).rejects.toThrow("The control plane returned 500 issuing a machine access token.");
	});

	it("rejects a 200 that carries no access token rather than caching nothing usable", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(new Response(JSON.stringify({ token_type: "Bearer" }), { status: 200 }));
		const { tokens } = source({ fetchImpl });

		await expect(tokens.get()).rejects.toThrow("The control plane did not return a machine access token.");
	});

	it("retries after a failure instead of caching it", async () => {
		const fetchImpl = vi
			.fn()
			.mockResolvedValueOnce(errorBody(500, { error: "server_error" }))
			.mockResolvedValueOnce(okBody());
		const { tokens } = source({ fetchImpl });

		await expect(tokens.get()).rejects.toThrow(/500/);
		await expect(tokens.get()).resolves.toMatchObject({ token: MACHINE_TOKEN });
	});

	it("keeps both tokens out of every error it raises", async () => {
		for (const response of [errorBody(401, {}), errorBody(404, {}), errorBody(500, {})]) {
			const { tokens } = source({ fetchImpl: vi.fn().mockResolvedValue(response) });
			const err = await tokens.get().catch((e: unknown) => e);
			const text = `${(err as Error).name}: ${(err as Error).message}`;
			expect(text).not.toContain(CONTROL_TOKEN);
			expect(text).not.toContain(MACHINE_TOKEN);
		}
	});
});
