import { createFileRoute, redirect } from "@tanstack/react-router";

// Log drains were a tab of a project's settings; they are a section of
// its Settings page now. Old links (#drains-<environment>) land on it.
export const Route = createFileRoute("/_app/$org/$project/settings/log-drains")(
  {
    beforeLoad: ({ params, location }) => {
      const env = location.hash.match(/^drains-(.+)$/)?.[1];
      throw redirect({
        to: "/$org/$project/settings",
        params,
        search: env ? { env } : {},
        hash: "log-drains",
        replace: true,
      });
    },
  },
);
