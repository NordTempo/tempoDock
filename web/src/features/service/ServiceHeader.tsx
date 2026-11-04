import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useRestart, useRollback } from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  isDatabase,
  listenPort,
  useCanDeploy,
  useChanges,
  useDeployBlock,
  useEnvironment,
  useReleases,
  useService,
} from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { EnvSwitch } from "../../shell/context";
import { type Shortcut, useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { ago, plural, since } from "../../ui/format";
import {
  Act,
  Button,
  ErrorText,
  Glyph,
  PageBar,
  StateText,
} from "../../ui/kit";
import {
  failurePhrase,
  healthRestarts,
  isCrashLooping,
  isUnhealthy,
  oomPhrase,
  serviceState,
  stepPhrase,
} from "../../ui/status";
import { EnvServer } from "../servers/parts";
import { copiedConfig, errorSummary, rollbackTarget } from "./releases";
import { CpuUsage, MemoryUsage } from "./Usage";

const tabs = [
  { label: "Overview", to: "/$org/$project/$environment/$service", key: "g o" },
  // Database services only (isDatabase). G B, "base": G D is Domains.
  {
    label: "Data",
    to: "/$org/$project/$environment/$service/data",
    key: "g b",
  },
  // Stateful services only. G K, "keep": G B is Data.
  {
    label: "Backups",
    to: "/$org/$project/$environment/$service/backups",
    key: "g k",
  },
  {
    label: "Logs",
    to: "/$org/$project/$environment/$service/logs",
    key: "g l",
  },
  // One-off commands (ADR 0048). G C, "command": G R is Releases.
  {
    label: "Run",
    to: "/$org/$project/$environment/$service/run",
    key: "g c",
  },
  // CPU and memory. G M, from its old name Metrics: G R is Releases.
  {
    label: "Resources",
    to: "/$org/$project/$environment/$service/resources",
    key: "g m",
  },
  // Services with a domain only: what their domains served (ADR 0033).
  {
    label: "Traffic",
    to: "/$org/$project/$environment/$service/traffic",
    key: "g t",
  },
  // Services with a domain only: Dock Analytics' dashboard (ADR 0037).
  // G Y, "analytics": G A is Audit.
  {
    label: "Analytics",
    to: "/$org/$project/$environment/$service/analytics",
    key: "g y",
  },
  {
    label: "Releases",
    to: "/$org/$project/$environment/$service/releases",
    key: "g r",
  },
  {
    label: "Domains",
    to: "/$org/$project/$environment/$service/domains",
    key: "g d",
  },
  {
    label: "Variables",
    to: "/$org/$project/$environment/$service/variables",
    key: "g v",
  },
  {
    label: "Settings",
    to: "/$org/$project/$environment/$service/settings",
    key: "g s",
  },
  {
    label: "Audit",
    to: "/$org/$project/$environment/$service/audit",
    key: "g a",
  },
] as const;

/**
 * A service's header bar: project / service, the environment switch, its
 * state and, on the Overview tab, Deploy, Restart and Roll back.
 */
export function ServiceBar({
  serviceRef: ref,
  full,
  onDeploy,
}: {
  serviceRef: ServiceRef;
  /** With the actions, as on the Overview tab. */
  full: boolean;
  /** Opens the Deploy window (folder, image or GitHub). */
  onDeploy: () => void;
}) {
  const org = useOrg();
  const view = useService(ref);
  const state = serviceState(view);
  const serving = view.service.active_release;
  return (
    <PageBar
      parent={
        <Link
          to="/$org/$project"
          params={{ org, project: ref.project }}
          className="hover:text-fg"
        >
          {ref.project}
        </Link>
      }
      title={ref.service}
      after={
        <>
          <EnvSwitch
            project={ref.project}
            environment={ref.environment}
            service={ref.service}
          />
          <span className="text-xs">
            <StateText state={state} />
            {serving && state.tone === "ok" && (
              <span className="font-mono text-faint"> · r{serving}</span>
            )}
          </span>
        </>
      }
      actions={full && <Actions serviceRef={ref} onDeploy={onDeploy} />}
    />
  );
}

/**
 * The service's sections under its header bar, with ▲ where something
 * waits. G then a letter switches tabs.
 */
export function ServiceTabs({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const view = useService(ref);
  const changes = useChanges(ref);
  const navigate = useNavigate();
  const { service } = view;
  const shown = tabs.filter(
    (tab) =>
      (tab.label !== "Data" || isDatabase(service)) &&
      (tab.label !== "Backups" || service.stateful) &&
      ((tab.label !== "Traffic" && tab.label !== "Analytics") ||
        view.domains.length > 0),
  );
  useShortcuts(
    shown.map((tab) => ({
      key: tab.key,
      run: () => navigate({ to: tab.to, params: { org, ...ref } }),
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
    <nav
      aria-label="Service"
      className="tabs flex flex-wrap gap-0.5 border-b border-line px-4 py-2"
    >
      {shown.map((tab) => (
        <Link
          key={tab.to}
          to={tab.to}
          params={{ org, ...ref }}
          activeOptions={{ exact: true, includeSearch: false }}
          title={`${tab.label} (${tab.key.toUpperCase()})`}
        >
          {tab.label}
          {marks[tab.label] && <span className="text-warn"> ▲</span>}
        </Link>
      ))}
    </nav>
  );
}

/** Source, address, resources, port and server: the Overview's facts. */
export function ServiceFacts({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const view = useService(ref);
  const { server } = useEnvironment(ref).environment;
  const { service } = view;
  const domain =
    view.domains.find((d) => d.status === "active") ?? view.domains[0];
  const facts: [string, ReactNode][] = [
    [
      "Source",
      <span key="s" className="font-mono">
        {service.image ?? "built from source"}
      </span>,
    ],
    [
      "Address",
      domain ? (
        <a
          key="a"
          href={`https://${domain.hostname}`}
          className="font-mono text-info hover:underline"
        >
          {domain.hostname}
        </a>
      ) : (
        <Link
          key="a"
          to="/$org/$project/$environment/$service/domains"
          params={{ org, ...ref }}
          className="text-info hover:underline"
        >
          + Add domain
        </Link>
      ),
    ],
    [
      "Resources",
      <Link
        key="r"
        to="/$org/$project/$environment/$service/resources"
        params={{ org, ...ref }}
        title="Memory and CPU, latest sample against the limit"
        className="hover:text-fg"
      >
        <CpuUsage service={service} />
        <br />
        <MemoryUsage service={service} />
      </Link>,
    ],
    [
      "Port · server",
      <span key="p">
        <span className="font-mono">:{listenPort(service)}</span> ·{" "}
        <EnvServer name={server} />
      </span>,
    ],
  ];
  return (
    <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-panel border border-line bg-line @xl:grid-cols-4">
      {facts.map(([term, value]) => (
        <div key={term} className="flex flex-col gap-1 bg-page px-3.5 py-3">
          <dt className="text-2xs text-faint">{term}</dt>
          <dd className="min-w-0 break-words text-xs">{value}</dd>
        </div>
      ))}
    </dl>
  );
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
        {status.exit_code}
        {healthRestarts(status)}.{" "}
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
  } else if (isUnhealthy(status)) {
    sentence = (
      <>
        <span className="text-fail">✕</span> {mono(`r${serving}`)} runs but
        fails its health check
        {service.health_check_path && (
          <> {mono(`GET ${service.health_check_path}`)}</>
        )}
        {healthRestarts(status)}.{" "}
        {mut("Dock restarts it, with backoff. Check its logs.")}
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
        {status.health === "starting" && mut("Health check starting.")}{" "}
        {!!status.health_restarts &&
          mut(
            `Restarted ${plural(status.health_restarts, "time")} for failing health checks.`,
          )}{" "}
        {latest?.status === "failed" && latest.number > serving && (
          <span className="text-fail">
            r{latest.number} {failurePhrase(latest)} {ago(latest.created_at)}.
          </span>
        )}
      </>
    );
  }
  return <p className="text-md leading-normal">{sentence}</p>;
}

/** Deploy, restart, roll back: buttons with keys and CLI equivalents. */
function Actions({
  serviceRef: ref,
  onDeploy,
}: {
  serviceRef: ServiceRef;
  onDeploy: () => void;
}) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const mayDeploy = useCanDeploy(ref);
  const restart = useRestart();
  const rollback = useRollback();
  const target = rollbackTarget(releases);
  const [confirming, setConfirming] = useState(false);
  // An unreachable server blocks every action that starts a release.
  const blocked = useDeployBlock(ref);
  const busy =
    blocked ?? (deploying ? `r${deploying.number} is deploying` : undefined);
  const flags = `-s ${ref.service} -e ${ref.environment}`;

  // A GitHub deploy queues behind a running one; the window says what applies.
  const canDeploy = mayDeploy && !blocked;
  const canRestart = !!service.active_release && !busy && !restart.isPending;
  const canRollback = !!target && !busy && !rollback.isPending;

  const shortcuts: Shortcut[] = [
    {
      key: "d",
      label: "deploy",
      disabled: !canDeploy,
      run: onDeploy,
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
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs">
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
    <div className="flex flex-wrap items-start gap-2">
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
      <Act
        primary
        cli={`dock deploy [dir | --image ${service.image || "<image>"}] ${flags}`}
        disabled={!canDeploy}
        title={
          blocked ??
          (mayDeploy
            ? "A folder, an image or the GitHub branch; or drop a folder here"
            : "Needs the deploy scope")
        }
        onClick={onDeploy}
      >
        Deploy <kbd>D</kbd>
      </Act>
      <span className="self-center text-xs">
        {blocked && <span className="text-fail">Disabled: {blocked}.</span>}{" "}
        <ErrorText error={restart.error ?? rollback.error} />
      </span>
    </div>
  );
}
