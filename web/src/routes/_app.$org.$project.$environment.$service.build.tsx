import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Build & run tab: the top of Settings now (ADR 0076); ?edit= stays.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/build",
)({
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/settings",
      params,
      search,
      replace: true,
    });
  },
});
