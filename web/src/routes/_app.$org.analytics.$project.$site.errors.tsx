import { createFileRoute, redirect } from "@tanstack/react-router";
import { sitesQuery } from "../features/analytics/loaders";

// A site's old Errors view: its browser errors are issues on the Errors
// page (ADR 0062), so this goes there, picked to the site's environment.
export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/errors",
)({
  beforeLoad: async ({ context: { queryClient }, params }) => {
    const sites = await queryClient.ensureQueryData(sitesQuery(params.project));
    const site = sites.find((s) => s.name === params.site);
    throw redirect({
      to: "/$org/errors",
      params: { org: params.org },
      search: { project: params.project, environment: site?.environment },
      replace: true,
    });
  },
});
