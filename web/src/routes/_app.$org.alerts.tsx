import { createFileRoute, redirect } from "@tanstack/react-router";

// The organization's old Alerts page: its alerts are in the Inbox now,
// where they go under Settings, Alerts (ADR 0076).
export const Route = createFileRoute("/_app/$org/alerts")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/$org/inbox", params, replace: true });
  },
});
