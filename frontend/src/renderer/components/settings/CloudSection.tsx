import { Cloud } from "lucide-react";
import { useUiStore } from "../../stores/ui-store";
import { Switch } from "../ui/switch";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

// Single opt-in toggle that reveals the CLOUD sessions section on the board
// alongside LOCAL. Persisted via the ui-store, defaults off. Off, the board
// looks exactly as it does today and no peer daemon fetch happens.
export function CloudSection() {
	const cloudEnabled = useUiStore((state) => state.cloudEnabled);
	const setCloudEnabled = useUiStore((state) => state.setCloudEnabled);

	return (
		<SettingsSection title="Cloud" sectionId="cloud">
			<SettingsRow icon={Cloud} label="Turn on Cloud">
				<Switch aria-label="Turn on Cloud" checked={cloudEnabled} onCheckedChange={setCloudEnabled} />
			</SettingsRow>
		</SettingsSection>
	);
}
