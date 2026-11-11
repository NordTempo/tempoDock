import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { sitesQuery } from "../features/analytics/loaders";
import { Hostnames } from "../features/analytics/ServiceSite";
import { DomainsPanel } from "../features/service/DomainsPanel";

// Settings' Domains view (ADR 0076): who can open them, the domains, then
// which of them Dock Analytics counts, with the switch.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/domains",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Domains,
});

function Domains() {
  const ref = servicePath(Route.useParams());
  return (
    <>
      <DomainsPanel serviceRef={ref} />
      <Hostnames serviceRef={ref} />
    </>
  );
}
