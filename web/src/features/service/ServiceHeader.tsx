import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useEffect, useState } from "react";
import { useRollback } from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  listenPort,
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
import { EnvServer } from "../servers/parts";
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
            {serving && state.tone === "ok" && (
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
 * The service's sections under its header bar, with ▲ where something
 * waits. G then a letter switches tabs.
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
    // The release history on the Overview, and the service's site in the
    // Analytics area (the old tab's address redirects there), ADR 0059.
    {
      key: "g r",
      label: "releases",
      run: () =>
        navigate({
          to: "/$org/$project/$environment/$service",
          params: { org, ...ref },
          hash: "releases",
        }),
    },
    ...(view.domains.length > 0
      ? [
          {
            key: "g y",
            label: "analytics",
            run: () =>
              navigate({
                to: "/$org/$project/$environment/$service/analytics",
                params: { org, ...ref },
              }),
          },
        ]
      : []),
  ]);
  const mine = (c: { service?: string }) =>
    !c.service || c.service === ref.service;
  const marks: Partial<Record<ServiceTab["label"], boolean>> = {
    Domains:
      view.domains.some(domainNeedsAttention) ||
      changes.domains.some((d) => d.service === ref.service),
    Uptime: view.uptime?.state === "down",
    Variables: changes.variables.some(mine),
    "Build & run":
      changes.settings.some(mine) ||
      changes.volumes.some((v) => v.service === ref.service),
  };

  return (
    <nav
      aria-label="Service"
      className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap [scrollbar-width:thin]"
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
        <span className="truncate" title={running}>
          {running ? shortImage(running) : "built from source"}
        </span>
        {image.differs && (
          <span className="font-sans break-all text-mut" title={image.next}>
            next deploy{" "}
            <span className="font-mono text-warn-text">
              {shortImage(image.next)}
            </span>
            {image.failed && (
              <>
                {" "}
                <Link
                  to="/$org/$project/$environment/$service/releases/$number"
                  params={{ org, ...ref, number: String(image.failed.number) }}
                  className="text-fail underline decoration-fail-edge underline-offset-2 hover:decoration-current"
                >
                  failed as r{image.failed.number}
                </Link>
              </>
            )}
          </span>
        )}
      </span>,
    ],
    [
      "Address",
      domain ? (
        <a
          key="a"
          href={`https://${domain.hostname}`}
          className="-my-[4px] inline-flex min-h-[24px] items-center font-mono text-info hover:underline"
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
    <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-panel border border-line bg-line @4xl:grid-cols-4">
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
  const org = useOrg();
  const mayDeploy = useCanDeploy(refOf(view.service));
  const { service, deploying, latest_release: latest } = view;
  // A release named in the sentence links to its page, which says why.
  const releaseLink = (number: number, text: string) => (
    <Link
      to="/$org/$project/$environment/$service/releases/$number"
      params={{ org, ...refOf(service), number: String(number) }}
      className="text-inherit underline decoration-faint underline-offset-2 hover:decoration-current"
    >
      {text}
    </Link>
  );
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
        {service.stopped_at
          ? mut(`Stopped: it runs again once ${r} is live.`)
          : serving
            ? mut(
                service.stateful
                  ? `r${serving} was stopped first: a volume allows one writer.`
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
        )}{" "}
        {latest?.status === "failed" && latest.number > (serving ?? 0) && (
          <span className="text-fail">
            {releaseLink(
              latest.number,
              `r${latest.number} ${failurePhrase(latest)} ${ago(latest.created_at)}.`,
            )}
          </span>
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
              className="lnk text-md text-info"
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
            {releaseLink(
              latest.number,
              `r${latest.number} ${failurePhrase(latest)} ${ago(latest.created_at)}.`,
            )}
          </span>
        )}
      </>
    );
  }
  return <p className="text-md leading-normal">{sentence}</p>;
}

/**
 * Stop or start, restart, roll back, deploy: buttons with keys and CLI
 * equivalents, and a menu with the rarer actions (Archive, for admins).
 * Stop, roll back and archive ask first, and restart does in production.
 * Buttons that can't apply (Roll back with nothing to go back to) are
 * left out; one line says why the others wait.
 */
function Actions({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const isAdmin = useIsAdmin();
  const onDeploy = () => openDeploy(ref);
  const { service, deploying, latest_release: latest } = useService(ref);
  const production = useEnvironment(ref).environment.kind === "production";
  const releases = useReleases(ref);
  const mayDeploy = useCanDeploy(ref);
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
  // An unreachable server blocks every action that starts a release.
  const blocked = useDeployBlock(ref);
  const busy =
    blocked ?? (deploying ? `r${deploying.number} is deploying` : undefined);
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const doRestart = () =>
    restart.mutate(
      { ref, stateful: service.stateful },
      {
        onSuccess: (r) => {
          setRestarting(r.number);
          setConfirming(null);
        },
      },
    );
  const askRestart = () =>
    production ? setConfirming("restart") : doRestart();

  // Once the restart's release serves (or fails), say so.
  useEffect(() => {
    if (restarting === null) return;
    if (service.active_release === restarting) {
      notify(`${ref.service} restarted, r${restarting} serving`);
      setRestarting(null);
    } else if (latest?.number === restarting && latest.status === "failed") {
      notify(`r${restarting} ${failurePhrase(latest)}`, "fail");
      setRestarting(null);
    }
  }, [restarting, service.active_release, latest, ref.service]);

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
    ...(stopped
      ? [
          {
            key: "s",
            label: "start",
            disabled: !canStart,
            run: () => start.mutate(ref),
          },
        ]
      : service.active_release
        ? [
            {
              key: "s",
              label: "stop",
              disabled: !canStop,
              run: () => setConfirming("stop"),
            },
          ]
        : []),
  ];
  useShortcuts(mayDeploy ? shortcuts : []);

  // Read-only callers see the state, not buttons they can't use.
  if (!mayDeploy) return null;

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
            : a volume allows one writer.
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
    <div className="flex flex-wrap items-start gap-2">
      {asking}
      {stopped ? (
        <Act
          cli={`dock service start ${ref.service} -e ${ref.environment}`}
          disabled={!canStart}
          title={busy ?? "Start a copy of the active release"}
          onClick={() => start.mutate(ref)}
        >
          Start <kbd>S</kbd>
        </Act>
      ) : (
        service.active_release && (
          <Act
            cli={`dock service stop ${ref.service} -e ${ref.environment}`}
            disabled={!canStop}
            title={busy ?? "Remove its container; domains answer 503"}
            onClick={() => setConfirming("stop")}
          >
            Stop <kbd>S</kbd>
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
          title={busy}
          onClick={() => setConfirming("rollback")}
        >
          Roll back to r{target.number} <kbd>B</kbd>
        </Act>
      )}
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
              className={menuItem}
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
