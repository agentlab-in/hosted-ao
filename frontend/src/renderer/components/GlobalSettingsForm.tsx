import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AccountSection } from "./settings/AccountSection";
import { CloudSection } from "./settings/CloudSection";
import { GeneralSettingsSection } from "./settings/GeneralSettingsSection";
import { MachinesSection } from "./settings/MachinesSection";
import { ReportProblemDialog } from "./settings/ReportProblemDialog";
import { SettingsLinkRow } from "./settings/SettingsRow";
import { SettingsSection } from "./settings/SettingsSection";
import { UpdatesSection } from "./settings/UpdatesSection";

// ponytail: AO account/machines/cloud settings are hosted-only and not yet
// wired into react-i18next (their copy is hardcoded English below, matching
// the section components themselves). Localize together if/when the rest of
// the hosted settings UI gets translated.

export type GlobalSettingsSection = "general" | "updates" | "help" | "all";

export function GlobalSettingsForm({
	section = "all",
	onOpenKeyboardShortcuts,
	onOpenConnectMobile,
}: {
	section?: GlobalSettingsSection;
	onOpenKeyboardShortcuts?: () => void;
	onOpenConnectMobile?: () => void;
}) {
	const { t } = useTranslation();
	const [reportProblemOpen, setReportProblemOpen] = useState(false);
	// One section per page means the dialog header already names it, so the
	// page's leading heading would just repeat that title. Only "all" (no
	// single-page header) shows every section's own heading.
	const leadingTitleHidden = section !== "all";

	return (
		<>
			<div
				aria-label={t("settings.title")}
				className="flex w-full flex-col gap-(--size-settings-section-gap)"
				data-testid="settings-page"
			>
				{(section === "all" || section === "general") && (
					<>
						<GeneralSettingsSection
							onConnectMobile={() => onOpenConnectMobile?.()}
							titleHidden={leadingTitleHidden}
						/>
						<AccountSection />
						<MachinesSection />
						<CloudSection />
						<SettingsSection title={t("settings.preferences")} grouped>
							<SettingsLinkRow
								label={t("settings.keyboardShortcuts")}
								onClick={() => onOpenKeyboardShortcuts?.()}
							/>
						</SettingsSection>
					</>
				)}
				{(section === "all" || section === "updates") && <UpdatesSection titleHidden={leadingTitleHidden} />}
				{(section === "all" || section === "help") && (
					<SettingsSection title={t("settings.getHelp")} titleHidden={leadingTitleHidden} grouped>
						<SettingsLinkRow label={t("settings.reportProblem")} onClick={() => setReportProblemOpen(true)} />
					</SettingsSection>
				)}
			</div>
			<ReportProblemDialog open={reportProblemOpen} onOpenChange={setReportProblemOpen} />
		</>
	);
}
