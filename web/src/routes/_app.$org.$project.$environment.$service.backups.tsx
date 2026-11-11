import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Backups tab: Settings' Backups now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/backups",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/settings",
      params,
      hash: "backups",
      replace: true,
    });
  },
});
