import { createFileRoute, notFound, Outlet } from "@tanstack/react-router";
import { findService, queries, servicePath } from "../data/queries";
import { ServiceHeader } from "../features/service/ServiceHeader";

// Service layout: header, tabs and meta stay put while tabs swap below.
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
  return (
    <>
      <ServiceHeader serviceRef={ref} />
      <Outlet />
    </>
  );
}
