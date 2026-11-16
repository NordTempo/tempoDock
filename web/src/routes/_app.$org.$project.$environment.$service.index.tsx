import { createFileRoute, Link } from "@tanstack/react-router";
import { servicePath, useRelease, useService } from "../data/queries";
import type { ServiceRef } from "../data/types";
import { RestoredFrom } from "../features/backups/Restored";
import { ConnectHint } from "../features/connect/ConnectHint";
import { NeedsAttention } from "../features/service/Attention";
import { ReleaseHistory } from "../features/service/ReleaseHistory";
import { imageLabel } from "../features/service/releases";
import {
  ServiceFacts,
  StatusSentence,
} from "../features/service/ServiceHeader";
import { Signals } from "../features/service/Signals";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useOrg } from "../shell/location";
import { useNow } from "../ui/clock";
import { elapsed } from "../ui/format";
import { Actor, actorKind, SectionHead } from "../ui/kit";
import { inFlight } from "../ui/status";

// A service at a glance (A2): status, what needs attention, facts, uptime
// and visitors, the deploy in flight and the newest releases (all of them
// on History, ADR 0076).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/",
)({
  component: ServiceOverview,
});

function ServiceOverview() {
  const ref = servicePath(Route.useParams());
  const service = useService(ref);
  const current = service.deploying;

  // One column: status, facts, signals, the deploy in flight and the
  // release history. Logs have their own tab; a database's connection
  // lines are on its Data tab.
  return (
    <div className="@container flex min-w-0 flex-col gap-7">
      {/* What serves, then what needs a look, said once (ADR 0076). */}
      <div className="flex flex-col gap-3">
        <StatusSentence view={service} />
        <NeedsAttention serviceRef={ref} view={service} />
      </div>
      <ServiceFacts serviceRef={ref} />
      {service.service.template && <RestoredFrom serviceRef={ref} />}
      <Signals serviceRef={ref} />
      {/* `dock connect` reaches an app's set port, once it was deployed
          (before that, the status says "Not deployed yet." once). */}
      {service.service.port > 0 &&
        !service.service.template &&
        service.latest_release && <ConnectHint serviceRef={ref} />}
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
