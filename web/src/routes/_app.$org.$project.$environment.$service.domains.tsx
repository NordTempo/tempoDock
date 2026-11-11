import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Domains tab: Settings' Domains now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/domains",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/settings",
      params,
      hash: "domains",
      replace: true,
    });
  },
});
