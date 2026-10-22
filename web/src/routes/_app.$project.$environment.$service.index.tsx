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
import {
  ServiceFacts,
  StatusSentence,
} from "../features/service/ServiceHeader";
import { deploySteps, StepTable } from "../features/service/Steps";
import { elapsed } from "../ui/format";
import { Actor, actorKind, SectionHead } from "../ui/kit";

// A service at a glance (A2): status, facts, the deploy in flight and
// recent releases, with live logs beside them.
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
    <div className="flex flex-1 flex-wrap">
      <div className="@container flex min-w-0 flex-[1_1_480px] flex-col gap-7 px-7 pt-6 pb-10">
        <StatusSentence view={service} />
        <ServiceFacts serviceRef={ref} />
        {current && (
          <DeployProgress
            key={current.number}
            serviceRef={ref}
            number={current.number}
          />
        )}
        <section aria-labelledby="releases">
          <SectionHead
            id="releases"
            right={
              releases.length > 5 && (
                <Link
                  to="/$project/$environment/$service/releases"
                  params={ref}
                >
                  All {releases[0]?.number}
                </Link>
              )
            }
          >
            Deploys
          </SectionHead>
          <ReleasesTable
            serviceRef={ref}
            releases={releases.slice(0, 5)}
            deploying={!!current}
          />
        </section>
      </div>
      <section
        aria-labelledby="logs"
        className="flex min-w-0 flex-[1_1_440px] flex-col gap-2.5 border-t border-line px-4 pt-4 pb-6 min-[1000px]:border-t-0 min-[1000px]:border-l"
      >
        <h2 id="logs" className="text-sm font-semibold">
          Logs
        </h2>
        <LogsPane
          // A fresh stream per deploy step: the container may not exist yet.
          key={`${current?.number}:${current?.status}`}
          serviceRef={ref}
          filter={filter}
          onFilter={setFilter}
          height="h-[max(320px,calc(100vh-240px))]"
        />
      </section>
    </div>
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
