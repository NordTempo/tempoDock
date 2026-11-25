import { Link } from "@tanstack/react-router";
import { memo } from "react";
import { domainNeedsAttention } from "../../data/problems";
import { listenPort, serviceRef, useCanDeploy } from "../../data/queries";
import type {
  EnvironmentView,
  ReleaseSummary,
  ServiceView,
} from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { Icon } from "../../ui/icons";
import { Button, Panel, StateText } from "../../ui/kit";
import { domainState, serviceState, toneText } from "../../ui/status";
import { openCreate } from "../create/open";
import {
  PreviewData,
  PreviewLine,
  PullRequestLink,
} from "../previews/PreviewParts";
import { EnvServer } from "../servers/parts";
import { ArchivedServices } from "./Archived";
import { SleepState } from "./EnvironmentSleep";
import { hiddenFailures } from "./failed";

/**
 * The picked environment's services on the project page: a row per
 * service with its status, release and where it's reached. A preview
 * environment (ADR 0056) also shows its pull request and data. The
 * environment's actions are in the page header (ProjectHeader).
 */
export function EnvironmentTable({ env }: { env: EnvironmentView }) {
  const { environment, services } = env;
  const ref = { project: environment.project, environment: environment.name };
  const canDeploy = useCanDeploy(ref);
  const pr = environment.preview;
  const newService = () => openCreate({ kind: "service", env: ref });
  const hidden = hiddenFailures(services);
  const offline = environment.server_status !== "online";
  return (
    <section aria-label={`${environment.name} services`}>
      {(pr || offline || environment.sleep?.state === "asleep") && (
        <div className="mb-2.5 flex flex-wrap items-center gap-x-3 gap-y-1">
          {pr && <PullRequestLink pr={pr} />}
          <SleepState environment={environment} />
          {offline && (
            <span className="text-xs">
              <EnvServer environment={environment} />
            </span>
          )}
        </div>
      )}
      {pr && <PreviewLine env={ref} />}
      {pr && <PreviewData env={ref} canDeploy={canDeploy} />}
      {services.length === 0 ? (
        <p className="flex flex-wrap items-center gap-x-2.5 gap-y-2 border-y border-line py-3 text-mut">
          No services in {environment.name} yet.
          {canDeploy && (
            <Button small primary onClick={newService}>
              New service
            </Button>
          )}
          <span>
            {canDeploy ? "or deploy a directory with" : "Deploy one with"}{" "}
            <code className="font-mono text-fg">
              dock deploy -e {environment.name}
            </code>
          </span>
        </p>
      ) : (
        <Panel>
          <table className="table-fixed">
            <colgroup>
              <col className="w-[26%]" />
              <col className="w-[18%]" />
              <col className="w-[14%]" />
              <col />
              <col className="w-8" />
            </colgroup>
            <thead>
              <tr>
                <th>Service</th>
                <th>Status</th>
                <th>Release</th>
                <th>Reached at</th>
                <th>
                  <span className="sr-only">Open</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <ServiceRow
                  key={s.service.id}
                  view={s}
                  hidden={hidden.get(s.service.id)}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      <ArchivedServices env={env} />
    </section>
  );
}

/**
 * One service: its status, its release (with a failed deploy marked
 * beside it) and where it's reached: its domain, or the private address
 * other services use. `hidden` is a failed deploy a later rollback or
 * restart hides (failed.ts), marked until a newer deploy succeeds.
 */
const ServiceRow = memo(function ServiceRow({
  view,
  hidden,
}: {
  view: ServiceView;
  hidden?: ReleaseSummary;
}) {
  const org = useOrg();
  const {
    service,
    deploying,
    active_release: active,
    latest_release: latest,
  } = view;
  const ref = serviceRef(service);
  // The latest release if it failed, else one a rollback or restart hides.
  const failed =
    (!deploying && latest?.status === "failed" ? latest : undefined) ?? hidden;
  const [primary, ...rest] = [...view.domains].sort(
    (a, b) => Number(a.generated) - Number(b.generated),
  );
  const attention = rest.filter(domainNeedsAttention).length;

  return (
    <tr className={`row-link ${deploying ? "bg-prog-bg" : ""}`}>
      <td className="py-2.5">
        <Link
          to="/$org/$project/$environment/$service"
          params={{ org, ...ref }}
          className="row-main font-medium hover:underline"
        >
          {service.name}
        </Link>
      </td>
      <td className="py-2.5">
        <StateText state={serviceState(view)} />
        {service.staged === "archive" && (
          <div className="text-2xs text-mut">archive staged</div>
        )}
      </td>
      <td className="py-2.5 font-mono">
        {active ? `r${active.number}` : <span className="text-faint">·</span>}
        {deploying && <span className="text-prog"> → r{deploying.number}</span>}
        {/* A failed deploy is state here, a mark beside what serves; the
            problems above say what went wrong (a failure is said once). */}
        {failed && (
          <Link
            to="/$org/$project/$environment/$service/history/$number"
            params={{ org, ...ref, number: String(failed.number) }}
            title={`r${failed.number} failed ${ago(failed.created_at)}`}
            aria-label={`r${failed.number} failed`}
            className="ml-1.5 text-fail hover:underline"
          >
            r{failed.number} ✕
          </Link>
        )}
      </td>
      <td className="truncate py-2.5 font-mono">
        {primary ? (
          <>
            {primary.hostname}
            {rest.length > 0 && (
              <span className="text-mut"> +{rest.length}</span>
            )}
            {/* What a domain waits for, in words, not a bare mark. */}
            {primary.status !== "active" ? (
              <div
                className={`font-sans text-2xs ${toneText[domainState(primary).tone]}`}
              >
                {domainState(primary).label}
              </div>
            ) : (
              attention > 0 && (
                <div className="font-sans text-2xs text-warn">
                  {attention === 1
                    ? "1 domain needs attention"
                    : `${attention} domains need attention`}
                </div>
              )
            )}
          </>
        ) : (
          <>
            <span className="text-mut">
              {service.name}:{listenPort(service)}
            </span>{" "}
            <span className="font-sans text-xs text-faint">private</span>
          </>
        )}
      </td>
      <td className="row-go" aria-hidden>
        <Icon name="chevronRight" size={14} />
      </td>
    </tr>
  );
});
