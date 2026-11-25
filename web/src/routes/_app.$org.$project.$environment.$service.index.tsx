import { createFileRoute, Link } from "@tanstack/react-router";
import { servicePath, useRelease, useService } from "../data/queries";
import type { ServiceRef } from "../data/types";
import { RestoredFrom } from "../features/backups/Restored";
import { failedNews } from "../features/overview/pipeline";
import { NeedsAttention } from "../features/service/Attention";
import { EnvStrip } from "../features/service/EnvStrip";
import { FailedDeploy } from "../features/service/FailedDeploy";
import { ReleaseHistory } from "../features/service/ReleaseHistory";
import { imageLabel } from "../features/service/releases";
import {
  ServiceFacts,
  StatusSentence,
} from "../features/service/ServiceHeader";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useOrg } from "../shell/location";
import { useNow } from "../ui/clock";
import { elapsed } from "../ui/format";
import { Actor, actorKind, SectionHead } from "../ui/kit";
import { inFlight } from "../ui/status";

// A service at a glance (A2, ADR 0099): status, the service in its other
// environments (the pipeline strip), a failed deploy with its cause and
// fix, what else needs attention, facts that each open their one home
// (uptime and visitors among them), the deploy in flight and the three
// newest releases (all of them on History). Connecting from your machine
// is in the header's "···" menu.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/",
)({
  component: ServiceOverview,
});

function ServiceOverview() {
  const ref = servicePath(Route.useParams());
  const service = useService(ref);
  const current = service.deploying;
  const failed = failedNews(service);

  // One column: status, facts, the deploy in flight and the newest
  // releases. Logs have their own tab; a database's connection lines are
  // on its Data tab.
  return (
    <div className="@container flex min-w-0 flex-col gap-7">
      {/* What serves, then what needs a look, said once (ADR 0076). */}
      <div className="flex flex-col gap-3">
        <StatusSentence view={service} />
        <EnvStrip serviceRef={ref} />
        {failed && (
          <FailedDeploy serviceRef={ref} view={service} failed={failed} />
        )}
        <NeedsAttention serviceRef={ref} view={service} />
      </div>
      <ServiceFacts serviceRef={ref} />
      {service.service.template && <RestoredFrom serviceRef={ref} />}
      {current && (
        <DeployProgress
          key={current.number}
          serviceRef={ref}
          number={current.number}
        />
      )}
      <ReleaseHistory serviceRef={ref} deploying={!!current} short />
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
  const org = useOrg();
  const { service } = useService(ref);
  const release = useRelease(ref, number);
  const now = useNow(inFlight(release.status));
  const steps = deploySteps(
    release,
    service.active_release ?? null,
    service.stateful,
    now,
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
            {elapsed(
              (release.finished_at ? Date.parse(release.finished_at) : now) -
                Date.parse(release.created_at),
            )}
          </>
        }
        right={
          <Link
            to="/$org/$project/$environment/$service/logs"
            params={{ org, ...ref }}
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
