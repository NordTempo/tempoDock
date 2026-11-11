import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Fragment, type ReactNode, useEffect, useState } from "react";
import { useRollback } from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  envPath,
  listenPort,
  queries,
  serviceRef as refOf,
  useCanDeploy,
  useChanges,
  useDeployBlock,
  useEnvironment,
  useIsAdmin,
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
  Confirm,
  ErrorText,
  Glyph,
  Menu,
  menuItem,
  PageBar,
  StateText,
} from "../../ui/kit";
import { notify } from "../../ui/notice";
import {
  failurePhrase,
  healthRestarts,
  isCrashLooping,
  isUnhealthy,
  oomPhrase,
  serviceState,
  stepPhrase,
} from "../../ui/status";
import { openDeploy } from "../deploy/start";
import { sleepCli, useWakeEnvironment } from "../overview/EnvironmentSleep";
import { EnvServer } from "../servers/parts";
import { ApplyControl } from "../variables/ApplyBox";
import { ArchiveConfirm } from "./ArchiveConfirm";
import { useRestartService, useStartService, useStopService } from "./data";
import { RollbackConfirm } from "./ReleasesTable";
import {
  errorSummary,
  imageLabel,
  imageSetting,
  rollbackTarget,
  shortImage,
} from "./releases";
import { type ServiceTab, tabsFor } from "./tabs";
import { CpuUsage, MemoryUsage } from "./Usage";

/**
 * A service's header bar: project / service, the environment switch, its
 * state and, on the Overview tab, Stop or Start, Restart, Roll back and
 * Deploy.
 */
export function ServiceBar({ serviceRef: ref }: { serviceRef: ServiceRef }) {
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
          className="inline-flex min-h-[24px] items-center hover:text-fg"
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
            {serving && !view.deploying && !view.service.stopped_at && (
              <span className="font-mono text-faint"> · r{serving}</span>
            )}
            {view.service.staged === "archive" && (
              <span className="text-mut"> · archive staged</span>
            )}
          </span>
        </>
      }
      actions={<Actions serviceRef={ref} />}
    />
  );
}

/**
 * The service's sections under its header bar, with the staged dot where
 * changes wait to be applied and ▲ where something needs a look. G then
 * a letter switches tabs.
 */
export function ServiceTabs({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const view = useService(ref);
  const changes = useChanges(ref);
  const navigate = useNavigate();
  const shown = tabsFor(view);
  useShortcuts([
    ...shown.map((tab) => ({
      key: tab.key,
      label: tab.label.toLowerCase(),
      run: () => navigate({ to: tab.to, params: { org, ...ref } }),
    })),
    // S means settings everywhere: here, the Settings tab.
    {
      key: "s",
      label: "settings",
      run: () =>
        navigate({
          to: "/$org/$project/$environment/$service/settings",
          params: { org, ...ref },
        }),
    },
    ...(view.domains.length > 0
      ? [
          {
            key: "g y",
            label: "visitors",
            run: () =>
              navigate({
                to: "/$org/$project/$environment/$service/requests/visitors",
                params: { org, ...ref },
              }),
          },
        ]
      : []),
  ]);
  const mine = (c: { service?: string }) =>
    !c.service || c.service === ref.service;
  // Two marks: the staged-change dot ("1 change staged") for changes not
  // yet applied, and ▲ for something that needs a look. Each says which.
  const staged: Partial<Record<ServiceTab["label"], boolean>> = {
    Variables: changes.variables.some(mine),
    Settings:
      changes.settings.some(mine) ||
      changes.volumes.some((v) => v.service === ref.service) ||
      changes.domains.some((d) => d.service === ref.service),
  };
  const warnings: Partial<Record<ServiceTab["label"], string>> = {
    ...(view.domains.some(domainNeedsAttention) && {
      Settings: "a domain needs attention",
    }),
    ...(view.uptime?.state === "down" && { Metrics: "a check is down" }),
  };
  return (
    <nav
      aria-label="Service"
      className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap [scrollbar-width:thin]"
    >
      {shown.map((tab, i) => (
        <Fragment key={tab.to}>
          {i > 0 && shown[i - 1]?.group !== tab.group && (
            <span aria-hidden className="mx-1.5 my-1 w-px flex-none bg-edge" />
          )}
          <Link
            to={tab.to}
            params={{ org, ...ref }}
            // A tab stays marked on its own views (Requests' Visitors);
            // Overview, every tab's prefix, only on itself.
            activeOptions={{
              exact: tab.label === "Overview",
              includeSearch: false,
            }}
            title={`${tab.label} (${tab.key.toUpperCase()})`}
          >
            {tab.label}
            {/* A mark's name follows the label as its own phrase
                ("Settings, changes waiting to apply"), not run into it. */}
            {staged[tab.label] && (
              <span
                className="ml-1.5 text-warn"
                role="img"
                aria-label=", changes waiting to apply"
                title="Changes waiting to apply"
              >
                <span className="dot" />
              </span>
            )}
            {warnings[tab.label] && (
              <span
                className="ml-1 text-warn"
                role="img"
                aria-label={`, ${warnings[tab.label]}`}
                title={warnings[tab.label]}
              >
                ▲
              </span>
            )}
          </Link>
        </Fragment>
      ))}
    </nav>
  );
}

/** Image, address, resources, port and server, start command: the Overview's facts. */
export function ServiceFacts({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const view = useService(ref);
  const { environment } = useEnvironment(ref);
  const { service } = view;
  const domain =
    view.domains.find((d) => d.status === "active") ?? view.domains[0];
  const image = imageSetting(service, useReleases(ref));
  const running = view.active_release
    ? imageLabel(view.active_release)
    : service.image;
  const facts: [string, ReactNode][] = [
    [
      "Image",
      // What runs now; the image setting only when the next deploy differs
      // (after a rollback, or a failed deploy of a new tag).
      <span key="s" className="flex flex-col font-mono">
        <span className="break-all" title={running}>
          {/* The full name, but a digest only as long as it needs to be. */}
          {running?.replace(/@sha256:([0-9a-f]{12})[0-9a-f]+/, "@sha256:$1…") ||
            "built from source"}
        </span>
        {/* After a failed new tag, the status line says it once and the
            Deploy dialog offers the serving image; nothing more here. */}
        {image.differs && !image.failed && (
          <span className="font-sans break-words text-mut" title={image.next}>
            Next deploy uses{" "}
            <span className="font-mono text-sec">{shortImage(image.next)}</span>
          </span>
        )}
      </span>,
    ],
    [
      "Domains",
      // The address, how many more, and the way to all of them (Settings).
      <span key="a" className="flex flex-wrap items-center gap-x-2">
        {domain && (
          <a
            href={`https://${domain.hostname}`}
            className="-my-[4px] inline-flex min-h-[24px] items-center font-mono text-info hover:underline"
          >
            {domain.hostname}
          </a>
        )}
        <Link
          to="/$org/$project/$environment/$service/settings/domains"
          params={{ org, ...ref }}
          // A button, not faint text: it is where domains are added.
          className="btn sm -my-[4px]"
        >
          {domain
            ? view.domains.length > 1
              ? `+${view.domains.length - 1} · Manage`
              : "Manage"
            : "Add domain"}
        </Link>
      </span>,
    ],
    ...(service.stateful
      ? ([["Backups", <BackupsFact key="b" serviceRef={ref} />]] as [
          string,
          ReactNode,
        ][])
      : []),
    [
      "Resources",
      <Link
        key="r"
        to="/$org/$project/$environment/$service/metrics"
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
      // Only operators see which server; others see it only when down.
      environment.server ? "Port and server" : "Port",
      <span key="p">
        <span className="font-mono">:{listenPort(service)}</span>{" "}
        {environment.server && <span className="text-mut">on server</span>}{" "}
        <EnvServer environment={environment} />
      </span>,
    ],
    // How it starts, a full row: Jobs' Run a command runs a command
    // once; this is where the start command changes.
    [
      "Start command",
      <span key="c" className="flex flex-wrap items-center gap-x-3">
        {service.start_command ? (
          <LongCommand text={service.start_command} />
        ) : (
          <span className="text-mut">the image's default</span>
        )}
        <Link
          to="/$org/$project/$environment/$service/settings"
          params={{ org, ...ref }}
          search={{ edit: "start_command" }}
          className="btn sm"
        >
          Edit
        </Link>
      </span>,
    ],
  ];
  return (
    <dl className="facts grid grid-cols-2 gap-x-6 border-t border-line @4xl:grid-cols-4">
      {facts.map(([term, value]) => (
        <div
          key={term}
          className={`flex flex-col gap-1 border-b border-soft py-3 ${term === "Start command" ? "col-span-full" : ""}`}
        >
          <dt className="text-2xs text-faint">{term}</dt>
          <dd className="min-w-0 break-words text-xs">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * A command that can run long (a database's dozens of -c options): two
 * lines, wrapped at spaces, with Show all when there is more.
 */
function LongCommand({ text }: { text: string }) {
  const [all, setAll] = useState(false);
  const long = text.length > 140;
  return (
    <span className="flex min-w-0 flex-col items-start gap-1">
      <code
        title={text}
        className={`font-mono break-words ${long && !all ? "line-clamp-2" : ""}`}
      >
        {text}
      </code>
      {long && (
        <button type="button" className="lnk" onClick={() => setAll(!all)}>
          {all ? "Show less" : "Show all"}
        </button>
      )}
    </span>
  );
}

/** One sentence on what the service is doing and what happens next. */
export function StatusSentence({ view }: { view: ServiceView }) {
  const org = useOrg();
  const mayDeploy = useCanDeploy(refOf(view.service));
  const { service, deploying, latest_release: latest } = view;
  // A release named in the sentence links to its page, which says why.
  const releaseLink = (number: number, text: string) => (
    <Link
      to="/$org/$project/$environment/$service/history/$number"
      params={{ org, ...refOf(service), number: String(number) }}
      className="text-inherit underline decoration-faint underline-offset-2 hover:decoration-current"
    >
      {text}
    </Link>
  );
  const { status } = service;
  const serving = service.active_release;
  const mut = (text: string) => <span className="text-mut">{text}</span>;
  // The live release's diagnosis while it crash loops or is unhealthy
  // (ADR 0071): the likely cause, and the way to the evidence.
  const liveCause = (kind: "crash_loop" | "unhealthy") => {
    const d = view.active_release?.diagnosis;
    if (!d || d.kind !== kind || !d.rule || !serving) return null;
    return (
      <>
        {" "}
        Likely cause: {d.summary}. {releaseLink(serving, "See why")}
      </>
    );
  };
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
        {service.stopped_at
          ? mut(`Stopped: it runs again once ${r} is live.`)
          : serving
            ? mut(
                service.stateful
                  ? `r${serving} was stopped first: only one copy can use its volume at a time.`
                  : `r${serving} keeps serving until cutover. If the check fails, ${r} is stopped.`,
              )
            : mut("Nothing served yet.")}
      </>
    );
  } else if (service.stopped_at) {
    sentence = (
      <>
        <span className="text-mut">○</span> Stopped by{" "}
        {mono(service.stopped_by || "unknown")} {ago(service.stopped_at)}.{" "}
        {mut(
          `Its domains answer 503. ${serving ? `r${serving}, ` : ""}volumes and variables are kept.`,
        )}
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
        {liveCause("crash_loop")}
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
        {latest?.status === "failed" ? (
          <span className="text-mut">
            {releaseLink(
              latest.number,
              `r${latest.number} ${failurePhrase(latest)}: ${errorSummary(latest)}.`,
            )}
          </span>
        ) : (
          !latest &&
          mayDeploy && (
            <button
              type="button"
              className="lnk"
              onClick={() => openDeploy(refOf(service))}
            >
              Deploy it
            </button>
          )
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
        {liveCause("unhealthy") ??
          mut("Dock restarts it, with backoff. Check its logs.")}
      </>
    );
  } else {
    const domain = view.domains.find((d) => d.status === "active");
    // Until its ongoing health check first passes it is starting, as the
    // header and sidebar say.
    const starting = status.health === "starting";
    sentence = (
      <>
        {starting ? (
          <>
            <span className="text-prog">
              <Glyph char="◐" />
            </span>{" "}
            {mono(`r${serving}`)} is starting: its health check hasn't passed
            yet.
          </>
        ) : (
          <>
            <span className="text-ok">●</span> {mono(`r${serving}`)} is serving
            {status.updated_at &&
              (since(status.updated_at) === "now"
                ? ", just started"
                : `, up ${since(status.updated_at)}`)}
            .
          </>
        )}{" "}
        {domain && mut(`At ${domain.hostname}.`)}{" "}
        {!!status.health_restarts &&
          mut(
            `Restarted ${plural(status.health_restarts, "time")} for failing health checks.`,
          )}
      </>
    );
  }
  return (
    // The overview's focal point (chosen in a blind trial, 2026-11): what
    // serves and since when, larger than everything under it.
    <p className="text-[20px] leading-7 font-semibold [&_.text-mut]:font-normal">
      {sentence}
    </p>
  );
}

/**
 * A stateful service's last good backup, and the way to its restore
 * points (Settings): so backups are found from the Overview.
 */
function BackupsFact({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const runs = useQuery(queries.backups(ref)).data?.runs ?? [];
  const last = runs.find(
    (r) => r.operation === "backup" && r.status === "succeeded",
  );
  return (
    <Link
      to="/$org/$project/$environment/$service/settings/backups"
      params={{ org, ...ref }}
      className="-my-[4px] inline-flex min-h-[24px] items-center hover:underline"
    >
      {last?.finished_at ? `last ${ago(last.finished_at)}` : "none yet"}
      <span className="ml-2 text-mut">backups →</span>
    </Link>
  );
}

/**
 * Stop or start, restart, roll back, deploy: buttons with keys and CLI
 * equivalents, and a menu with the rarer actions (Archive, for admins).
 * Stop, roll back and archive ask first, and restart does in production.
 * Stop and Start have no key (S opens Settings, see ServiceTabs).
 * Buttons that can't apply (Roll back with nothing to go back to) are
 * left out; one line says why the others wait.
 */
function Actions({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const navigate = useNavigate();
  const isAdmin = useIsAdmin();
  const onDeploy = () => openDeploy(ref);
  const { service, deploying, latest_release: latest } = useService(ref);
  const production = useEnvironment(ref).environment.kind === "production";
  const releases = useReleases(ref);
  const mayDeploy = useCanDeploy(ref);
  // One primary per view: while changes wait, the header's Apply is it.
  const waiting = useChanges(ref).count > 0;
  const restart = useRestartService();
  // The release a restart started, until it serves or fails.
  const [restarting, setRestarting] = useState<number | null>(null);
  const rollback = useRollback();
  const stop = useStopService();
  const start = useStartService();
  const target = rollbackTarget(releases);
  const [confirming, setConfirming] = useState<
    "rollback" | "stop" | "archive" | "restart" | null
  >(null);
  const stopped = !!service.stopped_at;
  // Its environment is asleep: anyone who sees it may wake it, as opening
  // its address does.
  const wake = useWakeEnvironment();
  const envRef = { project: ref.project, environment: ref.environment };
  const wakeButton = service.status.state === "asleep" && (
    <Act
      cli={sleepCli("wake", envRef)}
      disabled={wake.isPending}
      title={`Start every service of ${ref.environment} again`}
      onClick={() => wake.mutate(envRef)}
    >
      {wake.isPending ? "Waking" : "Wake"}
    </Act>
  );
  // An unreachable server blocks every action that starts a release.
  const blocked = useDeployBlock(ref);
  const busy =
    blocked ?? (deploying ? `r${deploying.number} is deploying` : undefined);
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const doRestart = () =>
    restart.mutate(
      { ref },
      {
        onSuccess: (r) => {
          notify(`Restarting ${ref.service}…`, "info");
          setRestarting(r.number);
          setConfirming(null);
        },
      },
    );
  const askRestart = () =>
    production ? setConfirming("restart") : doRestart();

  // Once the restart's release serves (its health check passed) or fails,
  // say so: the same "starting until healthy" as the header and overview.
  const health = service.status.health;
  useEffect(() => {
    if (restarting === null) return;
    if (service.active_release === restarting && health !== "starting") {
      notify(
        health === "unhealthy"
          ? `r${restarting} runs but fails its health check`
          : `r${restarting} serving`,
        health === "unhealthy" ? "fail" : "ok",
      );
      setRestarting(null);
    } else if (latest?.number === restarting && latest.status === "failed") {
      const failed = restarting;
      notify(
        `r${failed} ${failurePhrase(latest)}${service.active_release ? `, r${service.active_release} still serving` : ""}`,
        "fail",
        {
          label: "See why",
          run: () =>
            navigate({
              to: "/$org/$project/$environment/$service/history/$number",
              params: { org, ...ref, number: String(failed) },
            }),
        },
      );
      setRestarting(null);
    }
  }, [restarting, service.active_release, health, latest, navigate, org, ref]);

  // A GitHub deploy queues behind a running one; the window says what applies.
  const canDeploy = mayDeploy && !blocked;
  const canRestart =
    !!service.active_release && !stopped && !busy && !restart.isPending;
  const canRollback = !!target && !busy && !rollback.isPending;
  // Stop and start need the server as well: busy covers an unreachable one.
  const canStop = !!service.active_release && !busy && !stop.isPending;
  const canStart = !busy && !start.isPending;

  const shortcuts: Shortcut[] = [
    {
      key: "d",
      label: "deploy",
      disabled: !canDeploy,
      run: onDeploy,
    },
    // Keys only for the buttons shown.
    ...(!stopped && service.active_release
      ? [
          {
            key: "r",
            label: "restart",
            disabled: !canRestart,
            run: askRestart,
          },
        ]
      : []),
    ...(target
      ? [
          {
            key: "b",
            label: "roll back",
            disabled: !canRollback,
            run: () => setConfirming("rollback"),
          },
        ]
      : []),
    // Stop and Start have no key: S opens Settings (settings
    // everywhere), and stopping is rare and destructive.
  ];
  useShortcuts(mayDeploy ? shortcuts : []);

  // Read-only callers see the state, not buttons they can't use.
  if (!mayDeploy) return wakeButton || null;

  const close = () => setConfirming(null);
  // The question in a modal over the page; the buttons stay where they are.
  const asking =
    confirming === "restart" ? (
      <Confirm
        title={`Restart ${ref.service} in ${ref.environment}?`}
        action={`Restart ${ref.service}`}
        cli={`dock restart ${flags}`}
        pending={!canRestart}
        error={restart.error}
        onConfirm={doRestart}
        onCancel={() => {
          restart.reset();
          close();
        }}
      >
        {service.stateful ? (
          <>
            A new release with r{service.active_release}'s image and config.{" "}
            <b>
              {ref.service} is down for a few seconds while r
              {service.active_release} stops
            </b>
            : only one copy can use its volume at a time.
          </>
        ) : (
          <>
            A new release with r{service.active_release}'s image and config
            starts next to r{service.active_release} and takes over once it
            passes its health check. {ref.service} stays reachable.
          </>
        )}
      </Confirm>
    ) : confirming === "stop" ? (
      <Confirm
        title={`Stop ${ref.service} in ${ref.environment}?`}
        action={`Stop ${ref.service}`}
        cli={`dock service stop ${ref.service} -e ${ref.environment}`}
        danger
        pending={!canStop}
        error={stop.error}
        onConfirm={() => stop.mutate(ref, { onSuccess: close })}
        onCancel={() => {
          stop.reset();
          close();
        }}
      >
        Its container is removed and its domains answer 503 until you start it
        again. Volumes and releases are kept.
      </Confirm>
    ) : confirming === "archive" ? (
      <ArchiveConfirm serviceRef={ref} onCancel={close} />
    ) : confirming === "rollback" && target ? (
      <RollbackConfirm
        serviceRef={ref}
        target={target}
        serving={releases.find((r) => r.status === "active")}
        blocked={!!busy}
        onDone={close}
      />
    ) : null;

  return (
    <div className="flex flex-wrap items-center gap-2">
      {asking}
      {/* Changes waiting to apply: their count and Apply, first in the row. */}
      <ApplyControl env={envPath(ref)} service={ref.service} />
      {wakeButton}
      {stopped ? (
        <Act
          // Stopped, starting it is the next step: it is the primary.
          primary={!waiting}
          cli={`dock service start ${ref.service} -e ${ref.environment}`}
          disabled={!canStart}
          title={busy ?? "Start a copy of the active release"}
          onClick={() => start.mutate(ref)}
        >
          Start
        </Act>
      ) : (
        service.active_release && (
          <Act
            cli={`dock service stop ${ref.service} -e ${ref.environment}`}
            disabled={!canStop}
            title={busy ?? "Remove its container; domains answer 503"}
            onClick={() => setConfirming("stop")}
          >
            Stop
          </Act>
        )
      )}
      {/* A stopped service starts instead; nothing deployed has nothing to restart. */}
      {!stopped && service.active_release && (
        <Act
          cli={`dock restart ${flags}`}
          disabled={!canRestart}
          title={busy ?? "Start a copy of the active release"}
          onClick={askRestart}
        >
          Restart <kbd>R</kbd>
        </Act>
      )}
      {target && (
        <Act
          cli={`dock rollback ${target.number} ${flags}`}
          disabled={!canRollback}
          title={
            busy ??
            `r${target.number} is the last release that served and differs from the one serving now`
          }
          onClick={() => setConfirming("rollback")}
        >
          {/* The target and why it is that one open in the dialog: a
              release number here read as "the one before" (it may not be). */}
          Roll back <kbd>B</kbd>
        </Act>
      )}
      <Act
        primary={!waiting && !stopped}
        cli={`dock deploy [dir | --image ${service.image || "<image>"}] ${flags}`}
        disabled={!canDeploy}
        title={
          blocked ??
          (!mayDeploy
            ? "Needs the deploy scope"
            : waiting
              ? "Apply is the next step; a deploy also takes the changes waiting to apply live"
              : "A folder, an image or the GitHub branch; or drop a folder here")
        }
        onClick={onDeploy}
      >
        Deploy <kbd>D</kbd>
      </Act>
      {isAdmin && (
        <Menu
          label="More actions"
          right
          button={
            <span className="btn" title="More actions">
              More
            </span>
          }
        >
          {(close) => (
            <button
              type="button"
              role="menuitem"
              className={`${menuItem} text-fail`}
              title={`dock service archive ${ref.service} -e ${ref.environment}`}
              onClick={() => {
                close();
                setConfirming("archive");
              }}
            >
              Archive {ref.service}…
            </button>
          )}
        </Menu>
      )}
      <span className="self-center text-xs">
        {blocked ? (
          <span className="text-fail">Disabled: {blocked}.</span>
        ) : (
          deploying && (
            <span className="text-faint">
              Waiting for r{deploying.number} to finish
            </span>
          )
        )}{" "}
        <ErrorText error={confirming ? null : (restart.error ?? start.error)} />
      </span>
    </div>
  );
}
