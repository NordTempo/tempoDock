import {
  createFileRoute,
  notFound,
  Outlet,
  useMatches,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { findService, queries, servicePath } from "../data/queries";
import { ServiceHeader } from "../features/service/ServiceHeader";
import { rememberProject } from "../shell/location";
import { Page } from "../ui/kit";

// Service layout: title and tabs stay put while tabs swap below.
// Addressed by name, like the API: /shop/production/api.
export const Route = createFileRoute("/_app/$project/$environment/$service")({
  loader: async ({ context: { queryClient }, params }) => {
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    if (!findService(overview, params)) throw notFound();
    await Promise.all([
      queryClient.ensureQueryData(queries.releases(params)),
      queryClient.ensureQueryData(queries.changes(params)),
      queryClient.ensureQueryData(queries.volumes(params)),
    ]);
  },
  component: ServiceLayout,
});

function ServiceLayout() {
  const ref = servicePath(Route.useParams());
  const onOverview = useMatches({
    select: (matches) =>
      matches.at(-1)?.routeId === "/_app/$project/$environment/$service/",
  });
  useEffect(() => rememberProject(ref.project), [ref.project]);
  return (
    <Page>
      <ServiceHeader serviceRef={ref} full={onOverview} />
      <Outlet />
    </Page>
  );
}
