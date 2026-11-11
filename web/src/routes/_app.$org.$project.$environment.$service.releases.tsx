import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Releases tab: History now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/releases",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/history",
      params,
      replace: true,
    });
  },
});
