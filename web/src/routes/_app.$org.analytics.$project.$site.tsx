import { createFileRoute } from "@tanstack/react-router";
import { analyticsSearch, sitesQuery } from "../features/analytics/loaders";
import { SitePage } from "../features/analytics/SitePage";

// A site in the Analytics area (ADR 0059): /nordtempo/analytics/shop/shop.example.com?range=7d&f=country:is:NO,
// its views (Overview, Globe, Vitals, Funnels, Replays) under it.
export const Route = createFileRoute("/_app/$org/analytics/$project/$site")({
  validateSearch: analyticsSearch,
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Site,
});

function Site() {
  const { project, site } = Route.useParams();
  return <SitePage project={project} name={site} search={Route.useSearch()} />;
}
