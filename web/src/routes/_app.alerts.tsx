import { createFileRoute } from "@tanstack/react-router";
import { queries, useIsAdmin } from "../data/queries";
import { AlertsPage } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";
import { Page, TitleRow } from "../ui/kit";

// Open alerts, their history and where they are sent (ADR 0020). Admins
// only: the API refuses everyone else.
export const Route = createFileRoute("/_app/alerts")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.scope === "admin") {
      await Promise.all([
        queryClient.ensureQueryData(alertQueries.open()),
        queryClient.ensureQueryData(alertQueries.all()),
        queryClient.ensureQueryData(alertQueries.channels()),
      ]);
    }
  },
  component: Alerts,
});

function Alerts() {
  if (!useIsAdmin()) {
    return (
      <Page>
        <TitleRow title="Alerts" />
        <p className="text-mut">Only admins see alerts.</p>
      </Page>
    );
  }
  return <AlertsPage />;
}
