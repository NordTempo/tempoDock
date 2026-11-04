import {
  createFileRoute,
  notFound,
  Outlet,
  useMatches,
} from "@tanstack/react-router";
import { useEffect } from "react";
import {
  findService,
  queries,
  servicePath,
  useCanDeploy,
} from "../data/queries";
import {
  DropOverlay,
  openDeploy,
  useDropTarget,
} from "../features/deploy/start";
import { ServiceBar, ServiceTabs } from "../features/service/ServiceHeader";
import { rememberProject } from "../shell/location";
import { Page } from "../ui/kit";

// Service layout: header bar and tabs stay put while tabs swap below.
// Addressed by name, like the API: /shop/production/api.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service",
)({
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

/**
 * Every tab of a service takes a dropped folder or .tar.gz and deploys it
 * (the Deploy window, like the header's Deploy).
 */
function ServiceLayout() {
  const ref = servicePath(Route.useParams());
  const tab = useMatches({ select: (matches) => matches.at(-1)?.routeId });
  // These lay out their own columns: logs beside the Overview, the scope
  // list beside Variables.
  const ownLayout =
    tab === "/_app/$org/$project/$environment/$service/" ||
    tab === "/_app/$org/$project/$environment/$service/variables";
  useEffect(() => rememberProject(ref.project), [ref.project]);
  // Each drop opens a fresh Deploy window (hosted by the shell).
  const dragging = useDropTarget(useCanDeploy(ref), (dropped) =>
    openDeploy(ref, { dropped }),
  );
  return (
    <>
      <ServiceBar serviceRef={ref} />
      <ServiceTabs serviceRef={ref} />
      {ownLayout ? (
        <Outlet />
      ) : (
        <Page
          width={
            tab === "/_app/$org/$project/$environment/$service/data"
              ? "max-w-none"
              : undefined
          }
        >
          <Outlet />
        </Page>
      )}
      {dragging && <DropOverlay service={ref.service} />}
    </>
  );
}
