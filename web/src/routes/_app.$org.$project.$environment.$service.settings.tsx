import { createFileRoute, Outlet } from "@tanstack/react-router";

// A service's Settings tab (ADR 0076, ADR 0099): one page of sections,
// the index route. /settings/domains and /settings/backups are older
// addresses of two of its sections and redirect to them.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings",
)({
  component: Outlet,
});
