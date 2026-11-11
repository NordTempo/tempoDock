import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import {
  queries,
  servicePath,
  useRelease,
  useReleases,
  useService,
} from "../data/queries";
import type { ServiceRef } from "../data/types";
import { RestoredFrom } from "../features/backups/Restored";
import { ConnectHint } from "../features/connect/ConnectHint";
import { NeedsAttention } from "../features/service/Attention";
import { ReleasesTable } from "../features/service/ReleasesTable";
import { imageLabel } from "../features/service/releases";
import {
  ServiceFacts,
  StatusSentence,
} from "../features/service/ServiceHeader";
import { Signals } from "../features/service/Signals";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useOrg } from "../shell/location";
import { elapsed } from "../ui/format";
import { Act, Actor, actorKind, ErrorText, SectionHead } from "../ui/kit";

// A service at a glance (A2): status, what needs attention, facts, uptime
// and visitors, the
// deploy in flight and the release history (ADR 0059: the Releases tab
// was folded in; G R and …/releases come here, #releases).
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
      {/* `dock connect` reaches an app's set port. */}
      {service.service.port > 0 && !service.service.template && (
        <ConnectHint serviceRef={ref} />
      )}
      {current && (
        <DeployProgress
          key={current.number}
          serviceRef={ref}
          number={current.number}
        />
      )}
      <ReleaseHistory serviceRef={ref} deploying={!!current} />
    </div>
  );
}

/** The most releases the API returns at once (`dock release list -n`). */
const MAX = 100;

/**
 * The release history with rollback and diff: the newest 10, the 20 the
 * service's release query holds on request, then up to 100.
 */
function ReleaseHistory({
  serviceRef: ref,
  deploying,
}: {
  serviceRef: ServiceRef;
  deploying: boolean;
}) {
  const newest = useReleases(ref);
  const [shown, setShown] = useState(10);
  const all = useQuery({ ...queries.releases(ref, MAX), enabled: shown > 20 });
  const releases = (all.data ?? newest).slice(0, shown);
  const more = shown < newest.length || (!all.data && newest.length === 20);
  return (
    <section aria-labelledby="releases">
      <SectionHead
        id="releases"
        aside={newest[0] && `${newest[0].number} in all`}
      >
        Releases
      </SectionHead>
      <ReleasesTable
        serviceRef={ref}
        releases={releases}
        deploying={deploying}
      />
      {more && (
        <div className="mt-2 flex items-center gap-3">
          <Act
            small
            cli={`dock release list -n ${MAX} -s ${ref.service} -e ${ref.environment}`}
            disabled={all.isFetching}
            onClick={() => setShown(shown < 20 ? 20 : MAX)}
          >
            {shown < 20 ? "Show more" : `Show up to ${MAX}`}
          </Act>
          <ErrorText error={all.error} />
        </div>
      )}
    </section>
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
