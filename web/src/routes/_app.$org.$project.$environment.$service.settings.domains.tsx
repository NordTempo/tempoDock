import { createFileRoute, redirect } from "@tanstack/react-router";

// The older address of Settings' Domains section (ADR 0076): the Settings
// tab is one page now (ADR 0099), so it opens there at #domains.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/domains",
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
