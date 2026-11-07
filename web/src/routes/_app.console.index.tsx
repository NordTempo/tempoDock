import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { alertQueries } from "../features/alerts/data";
import { backupSettingsQuery } from "../features/backups/settings";
import { ConsoleOverview } from "../features/console/OverviewPage";
import { registryQuery } from "../features/registry/data";

// The console's landing page: what needs attention and the numbers.
export const Route = createFileRoute("/_app/console/")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(alertQueries.open("platform")),
      queryClient.ensureQueryData(backupSettingsQuery()),
      queryClient.ensureQueryData(registryQuery()),
      queryClient.ensureQueryData(queries.users()),
      queryClient.ensureQueryData(queries.audit()),
    ]),
  component: ConsoleOverview,
});
