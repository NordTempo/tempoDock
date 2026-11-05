import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Releases tab: the history is on its Overview now
// (ADR 0059). A release's own page stays at …/releases/<number>.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/releases",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service",
      params,
      hash: "releases",
      replace: true,
    });
  },
});
