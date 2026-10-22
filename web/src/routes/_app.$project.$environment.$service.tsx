import {
  createFileRoute,
  notFound,
  Outlet,
  useMatches,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { findService, queries, servicePath } from "../data/queries";
import { ServiceBar, ServiceTabs } from "../features/service/ServiceHeader";
import { rememberProject } from "../shell/location";
import { Page } from "../ui/kit";

// Service layout: header bar and tabs stay put while tabs swap below.
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
  const tab = useMatches({ select: (matches) => matches.at(-1)?.routeId });
  const onOverview = tab === "/_app/$project/$environment/$service/";
  useEffect(() => rememberProject(ref.project), [ref.project]);
  return (
    <>
      <ServiceBar serviceRef={ref} full={onOverview} />
      <ServiceTabs serviceRef={ref} />
      {onOverview ? (
        // The Overview lays out its own columns, logs on the right.
        <Outlet />
      ) : (
        <Page
          width={
            tab === "/_app/$project/$environment/$service/data"
              ? "max-w-none"
              : undefined
          }
        >
          <Outlet />
        </Page>
      )}
    </>
  );
}
