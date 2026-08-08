import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type SessionUsageSummary = components["schemas"]["CompactSessionUsageResponse"];

export const sessionUsageQueryRoot = ["session-usage"] as const;
export const sessionUsageQueryKey = (projectId?: string) =>
	[...sessionUsageQueryRoot, projectId ?? "all"] as const;

export async function fetchSessionUsageSummaries(projectId?: string, signal?: AbortSignal): Promise<SessionUsageSummary[]> {
	const { data, error } = await apiClient.GET("/api/v1/usage/sessions", {
		params: { query: projectId ? { projectId } : {} },
		signal,
	});
	if (error) throw error;
	return data?.sessions ?? [];
}

export function sessionUsageQueryOptions(projectId?: string) {
	return {
		queryKey: sessionUsageQueryKey(projectId),
		queryFn: ({ signal }: { signal: AbortSignal }) => fetchSessionUsageSummaries(projectId, signal),
		retry: 1,
		select: (items: SessionUsageSummary[]) =>
			new Map(items.map((item) => [item.sessionId, item] as const)),
	};
}

export function useSessionUsageSummaries(projectId?: string) {
	return useQuery(sessionUsageQueryOptions(projectId));
}
