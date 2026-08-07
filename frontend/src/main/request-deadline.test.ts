import { describe, expect, it, vi } from "vitest";
import { fetchWithDeadline, withDeadline } from "./request-deadline";

/** A fetch that never settles, which is the failure this module exists for. */
const neverSettles: typeof fetch = () => new Promise<Response>(() => {});

describe("fetchWithDeadline", () => {
	it("rejects with a timeout when the request never settles", async () => {
		await expect(
			fetchWithDeadline(neverSettles, "https://cp.example/api/v1/machines", {}, 20, "Listing machines"),
		).rejects.toThrow(/Listing machines timed out/);
	});

	it("passes an abort signal through, so the socket is not left running", async () => {
		let seen: AbortSignal | undefined;
		const capture: typeof fetch = (_url, init) => {
			seen = (init as RequestInit).signal ?? undefined;
			return new Promise<Response>(() => {});
		};
		await expect(fetchWithDeadline(capture, "https://cp.example/x", {}, 20, "Probing")).rejects.toThrow(/timed out/);
		expect(seen?.aborted).toBe(true);
	});

	it("returns the response untouched on success and clears its timer", async () => {
		const ok = new Response("{}", { status: 200 });
		const fetchImpl: typeof fetch = async () => ok;
		await expect(fetchWithDeadline(fetchImpl, "https://cp.example/x", {}, 1000, "Listing machines")).resolves.toBe(ok);
	});

	it("propagates a real transport error rather than relabelling it a timeout", async () => {
		const boom: typeof fetch = async () => {
			throw new Error("ECONNREFUSED");
		};
		await expect(fetchWithDeadline(boom, "https://cp.example/x", {}, 1000, "Listing machines")).rejects.toThrow(
			"ECONNREFUSED",
		);
	});

	it("keeps the operation name out of the message when it would leak nothing", async () => {
		// The message reaches the UI, so it must name the operation and never the
		// URL, which can carry a token in a query string.
		await expect(
			fetchWithDeadline(neverSettles, "https://cp.example/x?token=SECRET", {}, 20, "Listing machines"),
		).rejects.toThrow(/^Listing machines timed out after \d+s\./);
		await expect(
			fetchWithDeadline(neverSettles, "https://cp.example/x?token=SECRET", {}, 20, "Listing machines"),
		).rejects.not.toThrow(/SECRET/);
	});
});

describe("withDeadline", () => {
	it("rejects when the wrapped work never settles", async () => {
		await expect(withDeadline(new Promise<string>(() => {}), 20, "Refreshing sign-in")).rejects.toThrow(
			/Refreshing sign-in timed out/,
		);
	});

	it("resolves with the work's value when it beats the deadline", async () => {
		await expect(withDeadline(Promise.resolve("token"), 1000, "Refreshing sign-in")).resolves.toBe("token");
	});

	it("does not leave a pending timer that would keep the process alive", async () => {
		vi.useFakeTimers();
		try {
			const clear = vi.spyOn(globalThis, "clearTimeout");
			await withDeadline(Promise.resolve(1), 60_000, "Refreshing sign-in");
			expect(clear).toHaveBeenCalled();
		} finally {
			vi.useRealTimers();
		}
	});
});
