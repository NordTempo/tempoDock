import { createFileRoute, redirect } from "@tanstack/react-router";

// The service's old Uptime tab: part of Metrics now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/uptime",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/metrics",
      params,
      hash: "uptime",
      replace: true,
    });
  },
});
