import { createFileRoute } from "@tanstack/react-router";
import { BackupSettingsPage } from "../features/backups/SettingsPage";
import { backupSettingsQuery } from "../features/backups/settings";
import { ConsoleTabs } from "../features/console/ConsoleTabs";

// Where backups go, retention, and the control plane's own backup.
export const Route = createFileRoute("/_app/console/backups")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(backupSettingsQuery()),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <BackupSettingsPage />
    </ConsoleTabs>
  );
}
