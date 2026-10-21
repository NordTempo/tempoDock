import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { servicePath, useReleases, useService } from "../data/queries";
import { DeployProgress } from "../features/service/DeployProgress";
import { LogsPane, noFilter } from "../features/service/LogsPane";
import { ReleasesTable } from "../features/service/ReleasesTable";
import { Heading } from "../ui/kit";

// Releases tab: deploy progress and history next to a live log tail.
export const Route = createFileRoute("/_app/$project/$environment/$service/")({
  component: ReleasesTab,
});

function ReleasesTab() {
  const ref = servicePath(Route.useParams());
  const service = useService(ref);
  const releases = useReleases(ref);
  const [filter, setFilter] = useState(noFilter);
  const current = service.deploying;

  return (
    <div className="grid grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)] border-b border-line">
      <div className="min-w-0 px-5 pb-4">
        {current && (
          <DeployProgress
            key={current.number}
            serviceRef={ref}
            number={current.number}
            serving={service.service.active_release ?? null}
          />
        )}
        <Heading>Releases</Heading>
        <ReleasesTable
          serviceRef={ref}
          releases={releases}
          deploying={!!current}
        />
      </div>
      <div className="min-w-0 border-l border-line px-5 pb-4">
        <Heading>Logs</Heading>
        <LogsPane
          serviceRef={ref}
          filter={filter}
          onFilter={setFilter}
          className="h-[332px]"
        />
      </div>
    </div>
  );
}
