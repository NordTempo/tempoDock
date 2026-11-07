import { createFileRoute } from "@tanstack/react-router";
import { useIsAdmin } from "../data/queries";
import { SiteDefaults } from "../features/analytics/AnalyticsConsole";
import { orgAnalyticsDefaultsQuery } from "../features/analytics/loaders";
import { OrgSettingsBar } from "../features/org/SettingsBar";
import { Page } from "../ui/kit";

// The org's defaults for its new analytics sites (ADR 0062), over the
// platform's. Existing sites keep their settings.
export const Route = createFileRoute("/_app/$org/settings/analytics")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(orgAnalyticsDefaultsQuery()),
  component: AnalyticsDefaults,
});

function AnalyticsDefaults() {
  const admin = useIsAdmin();
  return (
    <>
      <OrgSettingsBar />
      <Page>
        <p className="max-w-xl text-[13px] text-sec">
          New sites in this organization start with these settings. Existing
          sites keep theirs; change one on its own settings. What isn't set here
          comes from the platform.
        </p>
        {admin ? (
          <SiteDefaults scope="org" />
        ) : (
          <p className="text-mut">Ask an admin to change them.</p>
        )}
      </Page>
    </>
  );
}
