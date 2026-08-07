import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Loader2, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { DEFAULT_CONTROL_PLANE_URL } from "../../../shared/control-plane";
import type { AoAccountState } from "../../../shared/ao-account";
import { Button } from "../ui/button";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

export const aoAccountQueryKey = ["ao-account"] as const;

/**
 * AO account sign-in. Signing in is needed only to reach a machine you have
 * registered; everything on this computer keeps working with no account, so this
 * section never blocks anything and there is no sign-in wall anywhere else.
 */
export function AccountSection() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const query = useQuery({ queryKey: aoAccountQueryKey, queryFn: () => aoBridge.account.getState() });

	const apply = (next: AoAccountState) => queryClient.setQueryData(aoAccountQueryKey, next);

	const signIn = useMutation({
		mutationFn: () => aoBridge.account.signIn(),
		onSuccess: apply,
	});
	const signOut = useMutation({
		mutationFn: () => aoBridge.account.signOut(),
		onSuccess: apply,
	});

	const state = query.data;
	const status = state?.status ?? "signed-out";
	const busy = signIn.isPending || signOut.isPending || status === "signing-in";
	const unavailable = status === "unavailable";
	const signedIn = status === "signed-in";
	const mutationError = signIn.error ?? signOut.error;
	const error = state?.error ?? (mutationError instanceof Error ? mutationError.message : null);
	const nonDefaultControlPlane =
		state && state.controlPlaneUrl !== DEFAULT_CONTROL_PLANE_URL ? state.controlPlaneUrl : null;

	return (
		<SettingsSection title={t("settings.account.title")} sectionId="account">
			<SettingsRow icon={UserRound} label={t("settings.account.label")}>
				<div className="flex min-w-0 items-center gap-2">
					<span className="min-w-0 truncate text-control text-settings-muted" data-testid="ao-account-identity">
						{signedIn
							? state?.account?.email || state?.account?.id
							: busy
								? t("settings.account.waitingForBrowser")
								: t("settings.account.notSignedIn")}
					</span>
					{signedIn ? (
						<Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => signOut.mutate()}>
							{t("settings.account.signOut")}
						</Button>
					) : (
						<Button
							type="button"
							variant="primary"
							size="sm"
							disabled={busy || unavailable || query.isLoading}
							onClick={() => signIn.mutate()}
						>
							{busy ? <Loader2 className="size-icon-sm animate-spin" aria-hidden="true" /> : null}
							{t("settings.account.signIn")}
						</Button>
					)}
				</div>
			</SettingsRow>

			<p className="px-1 text-xs leading-row text-settings-muted">
				{signedIn ? t("settings.account.signOutHint") : t("settings.account.signInHint")}
			</p>

			{busy && !signedIn ? (
				<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.account.signInInProgress")}</p>
			) : null}

			{error ? (
				<p className="flex items-start gap-2 px-1 text-xs leading-row text-error" data-testid="ao-account-error">
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{error}</span>
				</p>
			) : null}

			{nonDefaultControlPlane ? (
				<p className="px-1 text-xs leading-row text-warning">
					{t("settings.account.devControlPlane")} <span className="font-mono">{nonDefaultControlPlane}</span> (AO_CONTROL_URL).
				</p>
			) : null}
		</SettingsSection>
	);
}
