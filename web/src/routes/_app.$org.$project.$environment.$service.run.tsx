import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old One-off command tab: the top of Jobs now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/run",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/jobs",
      params,
      hash: "run",
      replace: true,
    });
  },
});
