import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useDeploy, useRestart, useRollback } from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  isDatabase,
  useChanges,
  useProjectOverview,
  useReleases,
  useService,
} from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { type Shortcut, useShortcuts } from "../../shell/keys";
import { ago, since } from "../../ui/format";
import { Act, Button, ErrorText, Glyph, StateText } from "../../ui/kit";
import {
  failurePhrase,
  isCrashLooping,
  oomPhrase,
  serviceState,
  stepPhrase,
} from "../../ui/status";
import { copiedConfig, errorSummary, rollbackTarget } from "./releases";
import { CpuUsage, MemoryUsage } from "./Usage";

const tabs = [
  { label: "Overview", to: "/$project/$environment/$service", key: "g o" },
  // Database services only (isDatabase). G B, "base": G D is Domains.
  { label: "Data", to: "/$project/$environment/$service/data", key: "g b" },
  { label: "Logs", to: "/$project/$environment/$service/logs", key: "g l" },
  {
    label: "Metrics",
    to: "/$project/$environment/$service/metrics",
    key: "g m",
  },
  {
    label: "Releases",
    to: "/$project/$environment/$service/releases",
    key: "g r",
  },
  {
    label: "Domains",
    to: "/$project/$environment/$service/domains",
    key: "g d",
  },
  {
    label: "Variables",
    to: "/$project/$environment/$service/variables",
    key: "g v",
  },
  {
    label: "Settings",
    to: "/$project/$environment/$service/settings",
    key: "g s",
  },
  { label: "Audit", to: "/$project/$environment/$service/audit", key: "g a" },
] as const;

/**
 * Title, plain-language status, actions (Overview tab only) and tabs of a
 * service. G then a letter switches tabs.
 */
export function ServiceHeader({
  serviceRef: ref,
  full,
}: {
  serviceRef: ServiceRef;
  /** With the status sentence and actions, as on the Overview tab. */
  full: boolean;
}) {
  const view = useService(ref);
  const changes = useChanges(ref);
  const navigate = useNavigate();
  const { service } = view;
  const shown = isDatabase(service)
    ? tabs
    : tabs.filter((tab) => tab.label !== "Data");
  useShortcuts(
    shown.map((tab) => ({
      key: tab.key,
      run: () => navigate({ to: tab.to, params: ref }),
    })),
  );
  const mine = (c: { service?: string }) =>
    !c.service || c.service === ref.service;
  const marks: Partial<Record<(typeof tabs)[number]["label"], boolean>> = {
    Domains: view.domains.some(domainNeedsAttention),
    Variables: changes.variables.some(mine),
    Settings:
      changes.settings.some(mine) ||
      changes.volumes.some((v) => v.service === ref.service),
  };

  return (
    <section aria-labelledby="service" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5">
        <h1 id="service" className="text-xl font-semibold">
          {service.name}
        </h1>
        <span className="text-mut">
          {ref.environment}
          {full && (
            <>
              {" "}
              · <span className="font-mono">:{service.port || 8080}</span> ·{" "}
              <Link
                to="/$project/$environment/$service/metrics"
                params={ref}
                className="font-mono text-white no-underline"
                title="Memory and CPU, latest sample against the limit"
              >
                <MemoryUsage service={service} /> ·{" "}
                <CpuUsage service={service} />
              </Link>
            </>
          )}
        </span>
        <span className="ml-auto flex gap-4">
          <Siblings serviceRef={ref} />
        </span>
      </div>
      {full && <StatusSentence view={view} />}
      {full && <Actions serviceRef={ref} />}
      <nav
        aria-label="Service"
        className="tabs mt-2.5 flex flex-wrap gap-x-[22px] gap-y-1 border-b border-line"
      >
        {shown.map((tab) => (
          <Link
            key={tab.to}
            to={tab.to}
            params={ref}
            activeOptions={{ exact: true, includeSearch: false }}
            title={`${tab.label} (${tab.key.toUpperCase()})`}
          >
            {tab.label}
            {marks[tab.label] && <span className="text-warn"> ▲</span>}
          </Link>
        ))}
      </nav>
    </section>
  );
}

/** The same service in the other environments: "staging: ● r92". */
function Siblings({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { environments } = useProjectOverview(ref.project);
  return environments.flatMap(({ environment, services }) => {
    if (environment.name === ref.environment) return [];
    const twin = services.find((s) => s.service.name === ref.service);
    if (!twin) return [];
    const state = serviceState(twin);
    return [
      <Link
        key={environment.id}
        to="/$project/$environment/$service"
        params={{ ...ref, environment: environment.name }}
        className="text-mut"
      >
        {environment.name}: <StateText state={{ ...state, label: "" }} />
        <span className="font-mono">
          {twin.service.active_release
            ? `r${twin.service.active_release}`
            : "none"}
        </span>
      </Link>,
    ];
  });
}

/** One sentence on what the service is doing and what happens next. */
export function StatusSentence({ view }: { view: ServiceView }) {
  const { service, deploying, latest_release: latest } = view;
  const { status } = service;
  const serving = service.active_release;
  const mut = (text: string) => <span className="text-mut">{text}</span>;
  const mono = (text: string) => (
    <b className="font-mono font-medium">{text}</b>
  );

  let sentence: ReactNode;
  if (deploying) {
    const r = `r${deploying.number}`;
    sentence = (
      <>
        <span className="text-prog">
          <Glyph char="◐" />
        </span>{" "}
        Deploying {mono(r)}, {stepPhrase[deploying.status]}.{" "}
        {serving
          ? mut(
              service.stateful
                ? `r${serving} was stopped first: a volume allows one writer.`
                : `r${serving} keeps serving until cutover. If the check fails, ${r} is stopped.`,
            )
          : mut("Nothing served yet.")}
      </>
    );
  } else if (isCrashLooping(status)) {
    sentence = (
      <>
        <span className="text-fail">↻</span> Crash looping:{" "}
        {mono(`r${serving}`)} restarted {status.restart_count} times, last exit{" "}
        {status.exit_code}.{" "}
        {status.oom_killed && mut(`It ${oomPhrase(status)}.`)}
      </>
    );
  } else if (status.state === "crashed") {
    sentence = (
      <>
        <span className="text-fail">✕</span> {mono(`r${serving}`)}{" "}
        {status.oom_killed
          ? oomPhrase(status)
          : `crashed with exit ${status.exit_code}`}
        . {mut("Restart it, or roll back.")}
      </>
    );
  } else if (!serving) {
    sentence = (
      <>
        <span className="text-mut">○</span> Not deployed yet.{" "}
        {latest?.status === "failed" &&
          mut(
            `r${latest.number} ${failurePhrase(latest)}: ${errorSummary(latest)}.`,
          )}
      </>
    );
  } else if (status.state !== "running") {
    sentence = (
      <>
        <span className="text-fail">○</span> {mono(`r${serving}`)} has no
        running container. {mut("Restart it, or roll back.")}
      </>
    );
  } else {
    const domain = view.domains.find((d) => d.status === "active");
    sentence = (
      <>
        <span className="text-ok">●</span> {mono(`r${serving}`)} is serving
        {status.updated_at &&
          (since(status.updated_at) === "now"
            ? ", just started"
            : `, up ${since(status.updated_at)}`)}
        . {domain && mut(`At ${domain.hostname}.`)}{" "}
        {latest?.status === "failed" && latest.number > serving && (
          <span className="text-fail">
            r{latest.number} {failurePhrase(latest)} {ago(latest.created_at)}.
          </span>
        )}
      </>
    );
  }
  return <p className="text-lg leading-normal">{sentence}</p>;
}

/** Deploy, restart, roll back: buttons with keys and CLI equivalents. */
function Actions({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const deploy = useDeploy();
  const restart = useRestart();
  const rollback = useRollback();
  const target = rollbackTarget(releases);
  const [confirming, setConfirming] = useState(false);
  const busy = deploying ? `r${deploying.number} is deploying` : undefined;
  const flags = `-s ${ref.service} -e ${ref.environment}`;

  const canDeploy = !!service.image && !busy && !deploy.isPending;
  const canRestart = !!service.active_release && !busy && !restart.isPending;
  const canRollback = !!target && !busy && !rollback.isPending;

  const shortcuts: Shortcut[] = [
    {
      key: "d",
      label: "deploy",
      disabled: !canDeploy,
      run: () => deploy.mutate(ref),
    },
    {
      key: "r",
      label: "restart",
      disabled: !canRestart,
      run: () => restart.mutate(ref),
    },
    {
      key: "b",
      label: "roll back",
      disabled: !canRollback,
      run: () => setConfirming(true),
    },
  ];
  useShortcuts(shortcuts);

  if (confirming && target) {
    return (
      <div className="flex flex-wrap items-center gap-2.5">
        <span>
          Roll back to <b className="font-mono font-medium">r{target.number}</b>
          : <span className="font-mono text-mut">{copiedConfig(target)}</span>
        </span>
        <Button
          primary
          disabled={!canRollback}
          onClick={() =>
            rollback.mutate(
              { ref, to: target.number },
              { onSuccess: () => setConfirming(false) },
            )
          }
        >
          Roll back
        </Button>
        <Button onClick={() => setConfirming(false)}>Cancel</Button>
        <ErrorText error={rollback.error} />
      </div>
    );
  }

  return (
    <div className="flex flex-wrap items-start gap-2.5">
      <Act
        primary
        cli={`dock deploy ${flags}`}
        disabled={!canDeploy}
        title={
          busy ??
          (service.image
            ? `Deploy ${service.image}`
            : "Built from source: deploy with dock deploy")
        }
        onClick={() => deploy.mutate(ref)}
      >
        Deploy <kbd>D</kbd>
      </Act>
      <Act
        cli={`dock restart ${flags}`}
        disabled={!canRestart}
        title={busy ?? "Start a copy of the active release"}
        onClick={() => restart.mutate(ref)}
      >
        Restart <kbd>R</kbd>
      </Act>
      <Act
        cli={`dock rollback ${target?.number ?? ""} ${flags}`.replace(
          "  ",
          " ",
        )}
        disabled={!canRollback}
        title={
          busy ?? (target ? undefined : "No earlier release to roll back to")
        }
        onClick={() => setConfirming(true)}
      >
        {target ? `Roll back to r${target.number}` : "Roll back"} <kbd>B</kbd>
      </Act>
      <span className="self-center">
        <ErrorText error={deploy.error ?? restart.error ?? rollback.error} />
      </span>
    </div>
  );
}
