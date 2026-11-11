import { createFileRoute, redirect } from "@tanstack/react-router";

// A release's old address: its page is under History now (ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service_/releases/$number",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/history/$number",
      params,
      replace: true,
    });
  },
});
