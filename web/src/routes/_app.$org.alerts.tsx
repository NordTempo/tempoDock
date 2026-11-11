import { createFileRoute } from "@tanstack/react-router";
import { AlertsPage } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";

// The organization's alerts: its projects' and services' (ADR 0062), for
// every member; channels for its admins.
export const Route = createFileRoute("/_app/$org/alerts")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(alertQueries.open("org")),
      queryClient.ensureQueryData(alertQueries.all("org")),
      queryClient.ensureQueryData(alertQueries.vulns()),
    ]),
  component: () => <AlertsPage scope="org" />,
});
