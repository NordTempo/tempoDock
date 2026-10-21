import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import {
  servicePath,
  useRelease,
  useReleases,
  useService,
} from "../data/queries";
import type { ServiceRef } from "../data/types";
import { LogsPane, noFilter } from "../features/service/LogsPane";
import { ReleasesTable } from "../features/service/ReleasesTable";
import { imageLabel } from "../features/service/releases";
import { deploySteps, StepTable } from "../features/service/Steps";
import { elapsed } from "../ui/format";
import { Actor, actorKind, SectionHead } from "../ui/kit";

// A service at a glance: the deploy in flight, logs, recent releases.
export const Route = createFileRoute("/_app/$project/$environment/$service/")({
  component: ServiceOverview,
});

function ServiceOverview() {
  const ref = servicePath(Route.useParams());
  const service = useService(ref);
  const releases = useReleases(ref);
  const [filter, setFilter] = useState(noFilter);
  const current = service.deploying;

  return (
    <>
      {current && (
        <DeployProgress
          key={current.number}
          serviceRef={ref}
          number={current.number}
        />
      )}
      <section aria-labelledby="logs">
        <SectionHead id="logs">Logs</SectionHead>
        <LogsPane
          // A fresh stream per deploy step: the container may not exist yet.
          key={`${current?.number}:${current?.status}`}
          serviceRef={ref}
          filter={filter}
          onFilter={setFilter}
          height="h-[300px]"
        />
      </section>
      <section aria-labelledby="releases">
        <SectionHead
          id="releases"
          right={
            releases.length > 5 && (
              <Link
                to="/$project/$environment/$service/releases"
                params={ref}
                className="text-mut"
              >
                All {releases[0]?.number}
              </Link>
            )
          }
        >
          Releases
        </SectionHead>
        <ReleasesTable
          serviceRef={ref}
          releases={releases.slice(0, 5)}
          deploying={!!current}
        />
      </section>
    </>
  );
}

/** The release in flight, step by step, live. */
function DeployProgress({
  serviceRef: ref,
  number,
}: {
  serviceRef: ServiceRef;
  number: number;
}) {
  const { service } = useService(ref);
  const release = useRelease(ref, number);
  const steps = deploySteps(
    release,
    service.active_release ?? null,
    service.stateful,
  );
  return (
    <section aria-labelledby="progress">
      <SectionHead
        id="progress"
        aside={
          <>
            by{" "}
            <Actor
              name={release.created_by}
              kind={actorKind(release.created_by)}
            />{" "}
            · <span className="font-mono">{imageLabel(release)}</span> ·{" "}
            {elapsed(Date.now() - Date.parse(release.created_at))}
          </>
        }
        right={
          <Link
            to="/$project/$environment/$service/logs"
            params={ref}
            search={{ release: number, build: true }}
          >
            Build log
          </Link>
        }
      >
        r{release.number} progress
      </SectionHead>
      <StepTable steps={steps} />
    </section>
  );
}
