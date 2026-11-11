import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Activity tab: History's second section now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/audit",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/history",
      params,
      hash: "activity",
      replace: true,
    });
  },
});
