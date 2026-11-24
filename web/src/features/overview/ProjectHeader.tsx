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
import { ServerDots } from "../../shell/context";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Icon } from "../../ui/icons";
import {
  Act,
  lockHolds,
  Menu,
  MoreItem,
  MoreMenu,
  MoreSeparator,
  menuItem,
  PageBar,
} from "../../ui/kit";
import { type Tone, toneText } from "../../ui/status";
import { sitesQuery } from "../analytics/loaders";
import { canCreateEnvironment } from "../create/access";
import { openCreate } from "../create/open";
import { MoveStrip } from "../move/MoveStrip";
import { previewsCli } from "../previews/data";
import { ClosePreview } from "../previews/PreviewParts";
import { LockForm, LockStrip } from "./EnvironmentLock";
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

/** A project page's three tabs; Settings is last, as on every page. */
export type ProjectTab = "services" | "activity" | "settings";

/**
 * The header every project page shares (Services, Activity, Settings):
 * the name, the environment switch, the lock, New service and a "···"
 * menu with everything else, then the tabs. Under it, whatever the
 * picked environment needs said or asked: its lock, a running move, and
 * the forms its menu opens.
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
  const locked = e?.lock && lockHolds(e.lock);

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
              {e &&
                (e.lock ? (
                  <span className="inline-flex items-center gap-1.5 rounded-tag border border-warn-edge bg-stage-bg px-2 py-0.5 text-xs text-warn-text">
                    <Icon name="lock" size={12} />
                    {locked ? "Locked" : "Lock paused"}
                  </span>
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
              <ServerDots project={project} />
            </>
          }
          actions={
            <>
              {newService && (
                <Act
                  primary
                  cli={`dock service create <name> -p ${project} -e ${target?.environment.name}`}
                  onClick={newService}
                >
                  New service <kbd>N</kbd>
                </Act>
              )}
              <MoreMenu label={`${project}: more actions`}>
                {(close) => (
                  <>
                    {overview.environments.length > 1 && (
                      <MoreItem close={close} keyHint="C" onClick={compare}>
                        Compare environments
                      </MoreItem>
                    )}
                    {e && canClone && (
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
                    {e && (canSetSleep || canSleep || (pr && canClone)) && (
                      <MoreSeparator />
                    )}
                    {e && canSetSleep && (
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
                    {e && canSleep && (
                      <MoreItem
                        close={close}
                        cli={sleepCli("sleep", ref)}
                        onClick={() => sleep.mutate(ref)}
                      >
                        Put {e.name} to sleep now
                      </MoreItem>
                    )}
                    {e && pr && canClone && (
                      <MoreItem
                        close={close}
                        cli={previewsCli(`close ${e.name}`, project)}
                        onClick={() => toggle("close")}
                      >
                        Close this preview
                      </MoreItem>
                    )}
                    {e && operator && (
                      <>
                        <MoreSeparator />
                        <MoreItem
                          close={close}
                          cli={envCli(`move ${e.name} --to <server>`)}
                          onClick={() => toggle("move")}
                        >
                          Move {e.name} to another server
                        </MoreItem>
                      </>
                    )}
                    {admin && (
                      <>
                        <MoreSeparator />
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
            </>
          }
        />
        <ProjectTabs project={project} environment={environment} tab={tab} />
      </div>
      {e && (
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
 * What the picked environment says or asks under the header: its lock
 * (with Unlock), a running move, and the form its "···" item opened.
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
  if (!e.lock && !form && !children) return null;
  return (
    <div className="flex w-full max-w-[1400px] flex-col px-4 pt-4 empty:hidden sm:px-7 [&>*:last-child]:mb-0">
      {e.lock && <LockStrip envRef={ref} lock={e.lock} />}
      {children}
      {form}
    </div>
  );
}

/**
 * The project's environments side by side, the picked one marked; a dot
 * on one with a problem, so trouble in staging shows while production is
 * open. Previews, which can be many, sit in one menu at the end; "+"
 * makes a new environment. Each keeps the tab you're on.
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
  const regular = environments.filter((e) => !e.environment.preview);
  const previews = environments.filter((e) => e.environment.preview);
  const current = previews.find((e) => e.environment.name === environment);
  const segment =
    "inline-flex min-h-[26px] items-center gap-1.5 px-2.5 text-ctl text-mut no-underline hover:text-fg aria-[current=true]:bg-sel aria-[current=true]:text-fg";
  return (
    <nav
      aria-label="Environment"
      className="inline-flex overflow-hidden rounded-ctl border border-edge"
    >
      {regular.map((e) => (
        <button
          key={e.environment.id}
          type="button"
          aria-current={e.environment.name === environment}
          onClick={() => go(e.environment.name)}
          className={`${segment} not-first:border-l not-first:border-edge`}
        >
          {e.environment.name}
          <ToneDot tone={tones.get(e.environment.name)} />
        </button>
      ))}
      {previews.length > 0 && (
        <Menu
          label="Previews"
          className="border-l border-edge"
          button={
            <span aria-current={!!current} className={segment}>
              {current ? current.environment.name : "previews"}
              <span className="text-faint">{previews.length}</span>
              <Icon name="chevronDown" size={12} />
            </span>
          }
        >
          {(close) =>
            previews.map((e) => (
              <button
                key={e.environment.id}
                type="button"
                role="menuitem"
                className={menuItem}
                onClick={() => {
                  close();
                  go(e.environment.name);
                }}
              >
                {e.environment.name}
                <ToneDot tone={tones.get(e.environment.name)} />
              </button>
            ))
          }
        </Menu>
      )}
      {canNew && (
        <button
          type="button"
          aria-label="New environment"
          title={`New environment (dock environment create <name> -p ${project})`}
          onClick={() => openCreate({ kind: "environment", project })}
          className={`${segment} ${environments.length > 0 ? "border-l border-edge" : ""}`}
        >
          +
        </button>
      )}
    </nav>
  );
}

/** A small dot in a problem's colour, named for screen readers. */
function ToneDot({ tone }: { tone: Tone | undefined }) {
  if (!tone) return null;
  return (
    <span
      role="img"
      aria-label={tone === "fail" ? ", has a problem" : ", needs a look"}
      className={`dot ${toneText[tone]}`}
    />
  );
}

/** Services, Activity and Settings, for the picked environment. */
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
    <nav
      aria-label="Project"
      className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap"
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
          {...(tab !== "settings" && here)}
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
