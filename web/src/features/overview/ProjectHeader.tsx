import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { lazy, type ReactNode, Suspense, useState } from "react";
import {
  canDeploy,
  queries,
  useIsAdmin,
  useSeesPlatform,
  useSession,
} from "../../data/queries";
import type { EnvironmentView } from "../../data/types";
import { envSegment, ServerDots } from "../../shell/context";
import { EnvButtons } from "../../shell/EnvButtons";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Icon } from "../../ui/icons";
import { Act, MoreItem, MoreMenu, MoreSeparator, PageBar } from "../../ui/kit";
import { sitesQuery } from "../analytics/loaders";
import { canCreateEnvironment } from "../create/access";
import { openCreate } from "../create/open";
import { MoveStrip } from "../move/MoveStrip";
import { previewsCli } from "../previews/data";
import { ClosePreview } from "../previews/PreviewParts";
import { LockForm, LockPill } from "./EnvironmentLock";
import {
  duration,
  SleepForm,
  sleepCli,
  useSleepEnvironment,
} from "./EnvironmentSleep";
import { lockRequestDone, useLockRequested } from "./lockRequest";
import { useProblemTones } from "./Problems";
import { ArchiveConfirm } from "./ProjectSettings";

const MovePanel = lazy(() => import("../move/MovePanel"));

/**
 * A project page's tabs: Services, Activity and Settings for the picked
 * environment, then Project settings for every environment (ADR 0099).
 */
export type ProjectTab = "services" | "activity" | "settings" | "project";

/**
 * The header every project page shares (Services, Activity, Settings,
 * Project settings), one row per scope. The project's row: its name,
 * servers, Project settings and a "···" with what changes the whole
 * project. The environment's row: the switch, its tabs, then its lock
 * (a pill that opens the lock's details and controls) or Lock, New
 * service and a "···" with what changes that environment. Under it,
 * whatever the picked environment needs said or asked: a running move,
 * and the forms its menu or Lock opens. On Project settings, which
 * no environment scopes, no environment is marked and its actions, lock
 * and strips stay away.
 */
export function ProjectHeader({
  project,
  environment,
  tab,
}: {
  project: string;
  /** The picked environment; undefined while the project has none. */
  environment: string | undefined;
  tab: ProjectTab;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const session = useSession();
  const admin = useIsAdmin();
  const operator = useSeesPlatform();
  const env = overview.environments.find(
    (e) => e.environment.name === environment,
  );
  const ref = { project, environment: environment ?? "" };
  // The project's analytics: the site for the picked environment, else
  // the first one the project has.
  const site = useQuery({
    ...sitesQuery(project),
    select: (sites) => {
      const live = sites.filter((x) => !x.archived);
      return (live.find((x) => x.environment === environment) ?? live[0])?.name;
    },
  }).data;
  const canNewEnv = canCreateEnvironment(session, project);
  // A new service goes to the picked environment, else one the caller may
  // deploy to; the dialog lets them pick another.
  const deployable = overview.environments.filter((e) =>
    canDeploy(session, { project, environment: e.environment.name }),
  );
  const target =
    deployable.find((e) => e.environment.name === environment) ?? deployable[0];
  const newService = target
    ? () =>
        openCreate({
          kind: "service",
          env: { project, environment: target.environment.name },
        })
    : undefined;
  const sleep = useSleepEnvironment();
  const [open, setOpen] = useState<
    "lock" | "sleep" | "close" | "move" | "archive" | null
  >(null);
  const toggle = (what: NonNullable<typeof open>) =>
    setOpen(open === what ? null : what);
  // The palette's "Lock <env>" opens the lock form here.
  if (useLockRequested(ref) && open !== "lock") {
    lockRequestDone();
    setOpen("lock");
  }
  const settings = () =>
    navigate({
      to: "/$org/$project/settings",
      params: { org, project },
      search: environment ? { env: environment } : {},
    });
  const compare = () =>
    navigate({ to: "/$org/$project/compare", params: { org, project } });
  useShortcuts([
    ...(overview.environments.length > 1
      ? [{ key: "c", label: "compare", run: compare }]
      : []),
    { key: "s", label: "settings", run: settings },
    ...(newService
      ? [{ key: "n", label: "new service", run: newService }]
      : []),
  ]);

  const e = env?.environment;
  const pr = e?.preview;
  const canClone = canCreateEnvironment(session, project);
  const mayDeploy = !!e && canDeploy(session, ref);
  // Production never sleeps; an asleep one wakes from its page.
  const canSleep =
    mayDeploy &&
    e?.kind === "non_production" &&
    e.sleep?.state !== "asleep" &&
    !!env?.services.some((s) => s.service.status.state === "running");
  const canSetSleep = admin && e?.kind === "non_production" && !pr;
  const envCli = (args: string) => `dock environment ${args} -p ${project}`;
  const projectWide = tab === "project";

  return (
    <>
      {/* Header and tabs stick together, so the tabs never slide under it. */}
      <div className="sticky top-(--shell-top) z-10 bg-page">
        <PageBar
          title={project}
          after={
            <>
              <EnvironmentSwitch
                project={project}
                environment={environment}
                tab={tab}
                canNew={canNewEnv}
              />
              <ServerDots project={project} />
            </>
          }
          actions={
            <>
              <Link
                to="/$org/$project/project-settings"
                params={{ org, project }}
                search={environment ? { env: environment } : {}}
                className="btn no-underline aria-[current=page]:bg-sel"
                {...(projectWide && { "aria-current": "page" as const })}
              >
                Project settings
              </Link>
              {(overview.environments.length > 1 || site || admin) && (
                <MoreMenu label={`${project}: more actions`}>
                  {(close) => (
                    <>
                      {overview.environments.length > 1 && (
                        <MoreItem close={close} keyHint="C" onClick={compare}>
                          Compare environments
                        </MoreItem>
                      )}
                      {site && (
                        <MoreItem
                          close={close}
                          onClick={() =>
                            navigate({
                              to: "/$org/analytics/$project/$site",
                              params: { org, project, site },
                            })
                          }
                        >
                          Analytics for {project}
                        </MoreItem>
                      )}
                      {admin && (
                        <>
                          {(overview.environments.length > 1 || site) && (
                            <MoreSeparator />
                          )}
                          <MoreItem
                            close={close}
                            danger
                            cli={`dock project archive ${project}`}
                            onClick={() => setOpen("archive")}
                          >
                            Archive {project}…
                          </MoreItem>
                        </>
                      )}
                    </>
                  )}
                </MoreMenu>
              )}
            </>
          }
        />
        <div className="flex flex-wrap items-center gap-x-2 gap-y-2 border-b border-line px-4 py-2 sm:px-7">
          <ProjectTabs project={project} environment={environment} tab={tab} />
          {!projectWide && (
            <div className="ml-auto flex flex-wrap items-center gap-2">
              {e &&
                (e.lock ? (
                  // Keyed: an open Unlock question never carries over to
                  // another environment.
                  <LockPill key={e.id} envRef={ref} lock={e.lock} />
                ) : (
                  admin && (
                    <Act
                      small
                      cli={envCli(`lock ${e.name}`)}
                      aria-expanded={open === "lock"}
                      onClick={() => toggle("lock")}
                    >
                      <Icon name="lock" size={12} /> Lock {e.name}
                    </Act>
                  )
                ))}
              {newService && (
                <Act
                  primary
                  cli={`dock service create <name> -p ${project} -e ${target?.environment.name}`}
                  onClick={newService}
                >
                  New service <kbd>N</kbd>
                </Act>
              )}
              {e && (canClone || canSetSleep || canSleep || operator) && (
                <MoreMenu label={`${e.name}: more actions`}>
                  {(close) => (
                    <>
                      {canClone && (
                        <MoreItem
                          close={close}
                          cli={envCli(`clone ${e.name} <name>`)}
                          onClick={() =>
                            openCreate({
                              kind: "environment",
                              project,
                              cloneFrom: e.name,
                            })
                          }
                        >
                          Clone {e.name} into a new environment
                        </MoreItem>
                      )}
                      {canClone && (canSetSleep || canSleep || pr) && (
                        <MoreSeparator />
                      )}
                      {canSetSleep && (
                        <MoreItem
                          close={close}
                          cli={`dock env sleep-after ${e.name} <duration>|off -p ${project}`}
                          onClick={() => toggle("sleep")}
                        >
                          Sleep after…
                          {!!e.sleep?.after_minutes && (
                            <span className="ml-1.5 font-mono text-2xs text-faint">
                              {duration(e.sleep.after_minutes)}
                            </span>
                          )}
                        </MoreItem>
                      )}
                      {canSleep && (
                        <MoreItem
                          close={close}
                          cli={sleepCli("sleep", ref)}
                          onClick={() => sleep.mutate(ref)}
                        >
                          Put {e.name} to sleep now
                        </MoreItem>
                      )}
                      {pr && canClone && (
                        <MoreItem
                          close={close}
                          cli={previewsCli(`close ${e.name}`, project)}
                          onClick={() => toggle("close")}
                        >
                          Close this preview
                        </MoreItem>
                      )}
                      {operator && (
                        <>
                          {(canClone || canSetSleep || canSleep) && (
                            <MoreSeparator />
                          )}
                          <MoreItem
                            close={close}
                            cli={envCli(`move ${e.name} --to <server>`)}
                            onClick={() => toggle("move")}
                          >
                            Move {e.name} to another server
                          </MoreItem>
                        </>
                      )}
                    </>
                  )}
                </MoreMenu>
              )}
            </div>
          )}
        </div>
      </div>
      {e && !projectWide && (
        <EnvironmentStrips env={env} open={open} onClose={() => setOpen(null)}>
          {e.moving && open !== "move" && (
            <MoveStrip
              env={ref}
              moving={e.moving}
              movingTo={e.moving_to}
              onDetails={() => setOpen("move")}
            />
          )}
        </EnvironmentStrips>
      )}
      {open === "archive" && (
        <ArchiveConfirm project={project} onClose={() => setOpen(null)} />
      )}
    </>
  );
}

/**
 * What the picked environment says or asks under the header: a running
 * move, and the form its "···" item or Lock opened.
 * Nothing when there's nothing to say.
 */
function EnvironmentStrips({
  env,
  open,
  onClose,
  children,
}: {
  env: EnvironmentView;
  open: string | null;
  onClose: () => void;
  children: ReactNode;
}) {
  const { environment: e } = env;
  const ref = { project: e.project, environment: e.name };
  const operator = useSeesPlatform();
  const form =
    (open === "lock" && !e.lock && (
      <LockForm envRef={ref} onDone={onClose} />
    )) ||
    (open === "sleep" && <SleepForm environment={e} onDone={onClose} />) ||
    (open === "close" && <ClosePreview env={ref} onDone={onClose} />) ||
    (open === "move" && (
      <Suspense fallback={null}>
        <MovePanel
          env={ref}
          server={e.server ?? ""}
          kind={e.kind}
          operator={operator}
        />
      </Suspense>
    ));
  if (!form && !children) return null;
  return (
    <div className="flex w-full max-w-[1400px] flex-col px-4 pt-4 empty:hidden sm:px-7 [&>*:last-child]:mb-0">
      {children}
      {form}
    </div>
  );
}

/**
 * The project's environments, next to its name in the header
 * (EnvButtons): a dot on one with a problem, so trouble in staging shows
 * while production is open; "+" makes a new environment. Each keeps the
 * tab you're on.
 */
function EnvironmentSwitch({
  project,
  environment,
  tab,
  canNew,
}: {
  project: string;
  environment: string | undefined;
  tab: ProjectTab;
  canNew: boolean;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const { environments } = useSuspenseQuery(queries.overview(project)).data;
  const tones = useProblemTones(project);
  // On Project settings no environment is picked; picking one opens its
  // Services.
  const off = tab === "project";
  const go = (name: string) =>
    tab === "settings"
      ? navigate({
          to: "/$org/$project/settings",
          params: { org, project },
          search: { env: name },
        })
      : navigate({
          to: "/$org/$project/$environment",
          params: { org, project, environment: name },
          search: tab === "activity" ? { tab } : {},
        });
  return (
    <EnvButtons
      nav
      environments={environments.map((e) => e.environment)}
      picked={off ? undefined : environment}
      onPick={go}
      tones={tones}
    >
      {canNew && (
        <button
          type="button"
          aria-label="New environment"
          title={`New environment (dock environment create <name> -p ${project})`}
          onClick={() => openCreate({ kind: "environment", project })}
          className={`${envSegment} ${environments.length > 0 ? "border-l border-edge" : ""}`}
        >
          +
        </button>
      )}
    </EnvButtons>
  );
}

/**
 * Services, Activity and Settings, for the picked environment, in the
 * environment's row. None is marked on Project settings.
 */
function ProjectTabs({
  project,
  environment,
  tab,
}: {
  project: string;
  environment: string | undefined;
  tab: ProjectTab;
}) {
  const org = useOrg();
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const staged = overview.environments.some((e) => e.pending_changes > 0);
  // Marked by the tab we're on, not by the address: Services and
  // Activity share one, and the project's own address shows Services.
  const here = { "aria-current": "page" as const };
  const exact = { exact: true, includeSearch: true };
  return (
    // py-[5px] gives back the links' -5px target margin (index.css), which
    // the sideways scroll would otherwise clip, underline and all.
    <nav
      aria-label="Project"
      className="tabs flex gap-0.5 overflow-x-auto py-[5px] whitespace-nowrap"
    >
      {environment ? (
        <>
          <Link
            to="/$org/$project/$environment"
            params={{ org, project, environment }}
            activeOptions={exact}
            {...(tab === "services" && here)}
          >
            Services
          </Link>
          <Link
            to="/$org/$project/$environment"
            params={{ org, project, environment }}
            search={{ tab: "activity" }}
            activeOptions={exact}
            {...(tab === "activity" && here)}
          >
            Activity
          </Link>
        </>
      ) : (
        <Link
          to="/$org/$project"
          params={{ org, project }}
          activeOptions={exact}
          {...(tab === "services" && here)}
        >
          Services
        </Link>
      )}
      <Link
        to="/$org/$project/settings"
        params={{ org, project }}
        search={environment ? { env: environment } : {}}
        title="Settings (S)"
        activeOptions={exact}
        {...(tab === "settings" && here)}
      >
        Settings
        {staged && (
          <span
            className="ml-1.5 text-warn"
            role="img"
            aria-label=", changes waiting to apply"
            title="Changes waiting to apply"
          >
            <span className="dot" />
          </span>
        )}
      </Link>
    </nav>
  );
}
