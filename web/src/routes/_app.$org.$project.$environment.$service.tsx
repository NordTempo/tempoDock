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
    if (!findService(overview, params))
      throw notFound({ routeId: "/_app/$org" });
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
  // Variables lays out its own columns: the scope list beside the table.
  const ownLayout =
    tab === "/_app/$org/$project/$environment/$service/variables";
  useEffect(() => rememberProject(ref.project), [ref.project]);
  // Each drop opens a fresh Deploy window (hosted by the shell).
  const dragging = useDropTarget(useCanDeploy(ref), (dropped) =>
    openDeploy(ref, { dropped }),
  );
  return (
    <>
      {/* Header and tabs stick together, so the tabs never slide under it. */}
      <div className="sticky top-(--shell-top) z-10 bg-page">
        <ServiceBar serviceRef={ref} />
        <ServiceTabs serviceRef={ref} />
      </div>
      {/* Every tab uses the same full width, so the edges don't jump between tabs. */}
      {ownLayout ? (
        <Outlet />
      ) : (
        <Page>
          <Outlet />
        </Page>
      )}
      {dragging && <DropOverlay service={ref.service} />}
    </>
  );
}
