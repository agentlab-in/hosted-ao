import { getApiBaseUrl, subscribeApiBaseUrl } from "../lib/api-client";
import { isRemoteDaemonBaseUrl } from "../../shared/remote-daemon";
import { lazy, Suspense, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { CloudSection } from "./settings/CloudSection";
import { MachinesSection } from "./settings/MachinesSection";
import type { GlobalSettingsSection as GlobalSettingsPage } from "../stores/ui-store";
import { GeneralSettingsSection } from "./settings/GeneralSettingsSection";
import { HarnessSettingsSection } from "./settings/HarnessSettingsSection";
import { CloudCredentialsSection } from "./settings/CloudCredentialsSection";
import { CodexAccountsSection } from "./settings/CodexAccountsSection";
import { ConnectMobileContent } from "./settings/ConnectMobileContent";
import { KeyboardShortcutsContent } from "./settings/KeyboardShortcutsContent";
import { MobileDevicesSection } from "./settings/MobileDevicesSection";
import { ReportProblemContent } from "./settings/ReportProblemContent";
import { SettingsSection } from "./settings/SettingsSection";
import { BrowserProfilesSection } from "./settings/BrowserProfilesSection";

// ponytail: Machine and cloud settings are not yet wired into react-i18next
// (their copy is hardcoded English below, matching the section components
// themselves). Localize together with the rest of the hosted settings UI.
const UpdatesSection = lazy(async () => {
  const module = await import("./settings/UpdatesSection");
  return { default: module.UpdatesSection };
});

export type GlobalSettingsSection = GlobalSettingsPage | "all";

/** Full-width panel for page-level content (forms, editors), matches the
 *  grouped-row surface so pages read as one coherent family. */
function SettingsContentPanel({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-md bg-[var(--color-bg-settings-row)] px-4 py-4">
      {children}
    </div>
  );
}

export function GlobalSettingsForm({
  section = "all",
}: {
  section?: GlobalSettingsSection;
}) {
  const { t } = useTranslation();
  const all = section === "all";
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
      {(all || section === "general") && (
        <>
          <GeneralSettingsSection titleHidden={titleHidden} />
          <CloudSection />
        </>
      )}

      {(all || section === "self-hosting") && <MachinesSection />}
      {localControls && (all || section === "harness") && (
        <HarnessSettingsSection titleHidden={titleHidden} />
      )}

      {localControls && (all || section === "agents") && (
        <CodexAccountsSection titleHidden={titleHidden} />
      )}

      {(all || section === "browserProfiles") && (
        <BrowserProfilesSection titleHidden={titleHidden} />
      )}

      {localControls && (all || section === "mobile") && (
        <SettingsSection title={t("settings.mobile")} titleHidden={titleHidden}>
          <div className="rounded-md bg-[var(--color-bg-settings-row)] px-4 pb-4 pt-0">
            <ConnectMobileContent active />
            <MobileDevicesSection />
          </div>
        </SettingsSection>
      )}

      {localControls && (all || section === "cloud") && (
        <CloudCredentialsSection titleHidden={titleHidden} />
      )}

      {(all || section === "shortcuts") && (
        <SettingsSection
          title={t("settings.keyboardShortcuts")}
          titleHidden={titleHidden}
        >
          <SettingsContentPanel>
            <KeyboardShortcutsContent active />
          </SettingsContentPanel>
        </SettingsSection>
      )}

      {(all || section === "updates") && (
        <Suspense
          fallback={<UpdatesSectionSkeleton titleHidden={titleHidden} />}
        >
          <UpdatesSection titleHidden={titleHidden} />
        </Suspense>
      )}

      {(all || section === "help") && (
        <SettingsSection
          title={t("settings.reportProblem")}
          titleHidden={titleHidden}
        >
          <SettingsContentPanel>
            <ReportProblemContent active />
          </SettingsContentPanel>
        </SettingsSection>
      )}
    </div>
  );
}

function UpdatesSectionSkeleton({ titleHidden }: { titleHidden: boolean }) {
  return (
    <section
      className="flex w-full flex-col gap-(--size-settings-section-inner-gap)"
      aria-busy="true"
    >
      {!titleHidden && (
        <div className="mx-3 h-4 w-16 animate-pulse rounded bg-foreground/8 motion-reduce:animate-none" />
      )}
      <div className="h-32 w-full animate-pulse rounded-(--radius-settings-panel) border border-[var(--color-border-settings-dialog)] bg-[var(--color-bg-settings-input)] motion-reduce:animate-none" />
    </section>
  );
}
