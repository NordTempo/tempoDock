import { createFileRoute } from "@tanstack/react-router";
import { BackupSettingsPage } from "../features/backups/SettingsPage";
import { backupSettingsQuery } from "../features/backups/settings";

// Where backups go, retention, and the control plane's own backup.
export const Route = createFileRoute("/_app/console/backups")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(backupSettingsQuery()),
  component: BackupSettingsPage,
});
