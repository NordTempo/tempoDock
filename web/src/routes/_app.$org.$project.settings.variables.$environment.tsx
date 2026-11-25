import { createFileRoute, redirect } from "@tanstack/react-router";

// An environment's shared variables were a tab of a project's settings;
// they are the Variables section of its Settings page now, with that
// environment picked.
export const Route = createFileRoute(
  "/_app/$org/$project/settings/variables/$environment",
)({
  beforeLoad: ({ params: { environment, ...params } }) => {
    throw redirect({
      to: "/$org/$project/settings",
      params,
      search: { env: environment },
      hash: "variables",
      replace: true,
    });
  },
});
