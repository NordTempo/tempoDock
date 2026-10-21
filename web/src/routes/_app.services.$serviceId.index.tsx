import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { queries, useReleases, useService } from "../data/queries";
import { DeployProgress } from "../features/service/DeployProgress";
import { LogsPane, noFilter } from "../features/service/LogsPane";
import { ReleasesTable } from "../features/service/ReleasesTable";
import { inFlightRelease } from "../features/service/releases";
import { Heading } from "../ui/kit";

// Releases tab: deploy progress and history next to a live log tail.
export const Route = createFileRoute("/_app/services/$serviceId/")({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.logs(params.serviceId)),
  component: ReleasesTab,
});

function ReleasesTab() {
  const { serviceId } = Route.useParams();
  const service = useService(serviceId);
  const releases = useReleases(serviceId);
  const [filter, setFilter] = useState(noFilter);
  const current = inFlightRelease(service, releases);

  return (
    <div className="grid grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)] border-b border-line">
      <div className="min-w-0 px-5 pb-4">
        {current && (
          <DeployProgress
            release={current}
            serving={service.activeRelease?.number ?? null}
          />
        )}
        <Heading>Releases</Heading>
        <ReleasesTable
          serviceId={serviceId}
          releases={releases}
          deploying={!!current}
        />
      </div>
      <div className="min-w-0 border-l border-line px-5 pb-4">
        <Heading note="live">Logs</Heading>
        <LogsPane
          serviceId={serviceId}
          inFlight={current?.number ?? null}
          filter={filter}
          onFilter={setFilter}
          className="h-[332px]"
        />
      </div>
    </div>
  );
}
