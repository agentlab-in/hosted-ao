import { Cloud } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useUiStore } from "../../stores/ui-store";
import { Switch } from "../ui/switch";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

// Single opt-in toggle that reveals the CLOUD sessions section on the board
// alongside LOCAL. Persisted via the ui-store, defaults off. Off, the board
// looks exactly as it does today and no peer daemon fetch happens.
export function CloudSection() {
	const { t } = useTranslation();
	const cloudEnabled = useUiStore((state) => state.cloudEnabled);
	const setCloudEnabled = useUiStore((state) => state.setCloudEnabled);

	return (
		<SettingsSection title={t("settings.cloud.title")} sectionId="cloud">
			<SettingsRow icon={Cloud} label={t("settings.cloud.turnOn")}>
				<Switch aria-label={t("settings.cloud.turnOn")} checked={cloudEnabled} onCheckedChange={setCloudEnabled} />
			</SettingsRow>
		</SettingsSection>
	);
}
