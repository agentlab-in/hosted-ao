import { Fragment, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { getApiBaseUrl, subscribeApiBaseUrl } from "../lib/api-client";
import { isRemoteDaemonBaseUrl } from "../../shared/remote-daemon";
import { useSyncExternalStore } from "react";
import type { GlobalSettingsSection as GlobalSettingsPage } from "../stores/ui-store";
import { globalSettingsItemsFor } from "./settings/settingsCatalog";

export type GlobalSettingsSection = GlobalSettingsPage | "all";

export function GlobalSettingsForm({
	cloudEnabled = true,
	section = "all",
}: {
	cloudEnabled?: boolean;
	section?: GlobalSettingsSection;
}) {
	const { t } = useTranslation();
	const all = section === "all";
	// Hosted pair-mode boundary: credential, installer, and mobile controls
	// only make sense against the local daemon, not a paired remote machine.
	const baseUrl = useSyncExternalStore(subscribeApiBaseUrl, getApiBaseUrl, getApiBaseUrl);
	const localControls = !isRemoteDaemonBaseUrl(baseUrl);
	if (!localControls && ["harness", "agents", "mobile", "cloud"].includes(section)) {
		return <p role="status">{t("settings.localControlsOnly")}</p>;
	}
	// One section per page means the dialog header already names it, so a
	// leading in-page heading would just repeat that title.
	const titleHidden = !all;

	return (
		<div
			aria-label={t("settings.title")}
			className="flex w-full flex-col gap-(--size-settings-section-gap)"
			data-testid="settings-page"
		>
			{globalSettingsItemsFor(section, { cloudEnabled }).map((item) => (
				<Fragment key={item.id}>
					<Suspense fallback={null}>{item.render(t, titleHidden)}</Suspense>
				</Fragment>
			))}
		</div>
	);
}
