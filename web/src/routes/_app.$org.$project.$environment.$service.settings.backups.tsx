import { createFileRoute, redirect } from "@tanstack/react-router";

// The older address of Settings' Backups section (ADR 0076): the Settings
// tab is one page now (ADR 0099), so it opens there at #backups.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/backups",
)({
  beforeLoad: ({ params, location }) => {
    // A failed backup's alert links #backup-<id>: its row, with its log.
    throw redirect({
      to: "/$org/$project/$environment/$service/settings",
      params,
      hash: location.hash.startsWith("backup-") ? location.hash : "backups",
      replace: true,
    });
  },
});
