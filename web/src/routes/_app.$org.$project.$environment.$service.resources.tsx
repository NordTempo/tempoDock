import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Resources tab: part of Metrics now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/resources",
)({
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/metrics",
      params,
      search,
      replace: true,
    });
  },
});
