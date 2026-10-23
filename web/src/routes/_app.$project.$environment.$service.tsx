import {
  createFileRoute,
  notFound,
  Outlet,
  useMatches,
} from "@tanstack/react-router";
import { useEffect, useState } from "react";
import {
  findService,
  queries,
  servicePath,
  useCanDeploy,
} from "../data/queries";
import {
  type DeployStart,
  DeployWindow,
  DropOverlay,
  useDropTarget,
} from "../features/deploy/start";
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

/**
 * Every tab of a service takes a dropped folder or .tar.gz and deploys it
 * (the Deploy window, like the header's Deploy).
 */
function ServiceLayout() {
  const ref = servicePath(Route.useParams());
  const tab = useMatches({ select: (matches) => matches.at(-1)?.routeId });
  const onOverview = tab === "/_app/$project/$environment/$service/";
  useEffect(() => rememberProject(ref.project), [ref.project]);
  // The open Deploy window; each drop opens a fresh one.
  const [deploy, setDeploy] = useState<{ start: DeployStart; n: number }>();
  const open = (start: DeployStart) =>
    setDeploy((d) => ({ start, n: (d?.n ?? 0) + 1 }));
  const dragging = useDropTarget(useCanDeploy(ref), (dropped) =>
    open({ dropped }),
  );
  return (
    <>
      <ServiceBar
        serviceRef={ref}
        full={onOverview}
        onDeploy={() => open({})}
      />
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
      {dragging && <DropOverlay service={ref.service} />}
      {deploy && (
        <DeployWindow
          key={deploy.n}
          serviceRef={ref}
          start={deploy.start}
          onClose={() => setDeploy(undefined)}
        />
      )}
    </>
  );
}
