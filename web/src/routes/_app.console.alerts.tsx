import { createFileRoute } from "@tanstack/react-router";
import { AlertsPage } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";

// Open alerts, their history and where they are sent (ADR 0020).
export const Route = createFileRoute("/_app/console/alerts")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(alertQueries.open()),
      queryClient.ensureQueryData(alertQueries.all()),
      queryClient.ensureQueryData(alertQueries.channels()),
    ]),
  component: AlertsPage,
});
