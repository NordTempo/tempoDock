import { createFileRoute } from "@tanstack/react-router";
import { AlertSettings } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";
import { OrgSettingsBar } from "../features/org/SettingsBar";
import { Page } from "../ui/kit";

// Where the organization's alerts go and what alerts (ADR 0062, ADR 0074):
// its vulnerability setting and, for admins, its channels. The alerts
// themselves are in the Inbox (ADR 0076).
export const Route = createFileRoute("/_app/$org/settings/alerts")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(alertQueries.vulns()),
  component: () => (
    <>
      <OrgSettingsBar />
      <Page>
        <AlertSettings scope="org" />
      </Page>
    </>
  ),
});
