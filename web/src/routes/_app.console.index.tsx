import { createFileRoute } from "@tanstack/react-router";
import { alertQueries } from "../features/alerts/data";
import { backupSettingsQuery } from "../features/backups/settings";
import { ConsoleOverview } from "../features/console/OverviewPage";
import { consoleOrgsQuery } from "../features/orgs/data";
import { registryQuery } from "../features/registry/data";
import { storageQuery } from "../features/storage/data";

// The console's landing page: is the platform healthy (ADR 0062).
export const Route = createFileRoute("/_app/console/")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(alertQueries.open("platform")),
      queryClient.ensureQueryData(backupSettingsQuery()),
      queryClient.ensureQueryData(registryQuery()),
      queryClient.ensureQueryData(storageQuery()),
      queryClient.ensureQueryData(consoleOrgsQuery()),
    ]),
  component: ConsoleOverview,
});
