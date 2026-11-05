import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { sitesQuery } from "../features/analytics/loaders";
import { Hostnames } from "../features/analytics/ServiceSite";
import { DomainsPanel } from "../features/service/DomainsPanel";

// A service's domains, then which of them Dock Analytics counts, with the
// switch (it was on the service's Analytics tab before ADR 0059).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/domains",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: DomainsTab,
});

function DomainsTab() {
  const ref = servicePath(Route.useParams());
  return (
    <>
      <DomainsPanel serviceRef={ref} />
      <Hostnames serviceRef={ref} />
    </>
  );
}
