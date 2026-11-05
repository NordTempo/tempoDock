import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Settings tab, Build & run now (ADR 0059).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/build",
      params,
      replace: true,
    });
  },
});
