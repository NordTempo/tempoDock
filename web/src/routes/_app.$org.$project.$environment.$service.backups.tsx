import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Backups tab: Settings' Backups section now (ADR 0099).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/backups",
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
