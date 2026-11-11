import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Backups tab: Settings' Backups view now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/backups",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/settings/backups",
      params,
      replace: true,
    });
  },
});
