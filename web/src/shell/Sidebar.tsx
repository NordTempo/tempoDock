import { useQuery } from "@tanstack/react-query";
import {
  Link,
  type LinkProps,
  useMatches,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useLogout } from "../data/mutations";
import {
  canDeploy,
  queries,
  serviceRef,
  useIsAdmin,
  useIsOperator,
  useOrgs,
  useProjects,
  useProjectTree,
  useServers,
  useSession,
} from "../data/queries";
import type {
  Environment,
  EnvironmentView,
  Org,
  ServiceView,
} from "../data/types";
import {
  type Alert,
  useOrgOpenAlerts,
  usePlatformOpenAlerts,
} from "../features/alerts/data";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import { isPending, useRequestsIfAdmin } from "../features/requests/data";
import { useUpdateNote } from "../features/update/data";
import { since } from "../ui/format";
import { Icon, type IconName } from "../ui/icons";
import { ErrorText, Glyph, LockMark, Menu, menuItem } from "../ui/kit";
import { serverState, serviceState, toneText } from "../ui/status";
import { modKey } from "./keys";
import { useCurrentContext, useOrg } from "./location";
import { usePalette } from "./palette";

/**
 * Which sidebar a page gets: the project tree (every organization page,
 * its settings included), or the platform console (operators: every page
 * under /console).
 */
export type ShellMode = "app" | "console";

export const useShellMode = (): ShellMode =>
  useMatches({
    select: (matches) =>
      matches.some((m) => m.routeId === "/_app/console") ? "console" : "app",
  });

/**
 * The left column on every page. Wide screens keep it in view while the
 * page scrolls; narrow ones put it above the page.
 */
export function Sidebar({ mode }: { mode: ShellMode }) {
  // Narrow screens fold the sidebar behind a button; moving on folds it.
  const [open, setOpen] = useState(false);
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [foldedAt, setFoldedAt] = useState(path);
  if (foldedAt !== path) {
    setFoldedAt(path);
    setOpen(false);
  }
  return (
    <nav
      aria-label={mode === "console" ? "Platform" : "Organization"}
      className={`flex flex-col gap-0.5 border-b border-line bg-side px-2 py-2.5 lg:sticky lg:w-[248px] lg:flex-none lg:overflow-y-auto lg:border-r lg:border-b-0 ${
        // Under the console's sticky amber bar.
        mode === "console"
          ? "lg:top-8 lg:h-[calc(100vh-2rem)]"
          : "lg:top-0 lg:h-screen"
      }`}
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="nav-row lg:hidden"
      >
        <Icon name="list" className="text-mut" />
        {open ? "Hide navigation" : "Navigation"}
      </button>
      <div
        // Rows never shrink below their height: a long tree scrolls instead.
        className={`${open ? "flex" : "hidden"} flex-1 flex-col gap-0.5 *:shrink-0 lg:flex`}
      >
        {mode === "app" ? <AppNav /> : <ConsoleNav />}
      </div>
    </nav>
  );
}

/** A sidebar link with an icon; current when its route (or a child) is. */
function NavLink({
  icon,
  children,
  exact,
  ...link
}: LinkProps & { icon?: IconName; exact?: boolean; children: ReactNode }) {
  return (
    <Link
      {...link}
      className="nav-row"
      activeOptions={{ exact: !!exact, includeSearch: false }}
      activeProps={{ "aria-current": "page" }}
    >
      {icon && <Icon name={icon} className="text-mut" />}
      {children}
    </Link>
  );
}

const groupLabel = "px-2.5 pt-3 pb-1 text-2xs font-medium text-faint";

function AppNav() {
  const openPalette = usePalette();
  const onMembers = useRouterState({
    select: (r) => r.location.pathname.endsWith("/settings/members"),
  });
  const operator = useIsOperator();
  const admin = useIsAdmin();
  return (
    <>
      <OrgSwitcher />
      <button
        type="button"
        onClick={() => openPalette()}
        className="mx-1 mb-2.5 flex items-center gap-2 rounded-[7px] border border-line bg-panel px-2.5 py-[7px] text-left text-mut hover:border-edge hover:text-sec"
      >
        <Icon name="search" />
        <span className="flex-1">Search or run…</span>
        <kbd>{modKey.trim()}K</kbd>
      </button>
      <div className="px-2.5 pt-1.5 pb-1 text-2xs font-medium text-faint">
        Projects
      </div>
      <ProjectTree />
      <ArchivedProjects />
      <div className="min-h-6 flex-1" />
      <NavLink to="/$org/analytics" icon="bars">
        Analytics
      </NavLink>
      <NavLink to="/$org/errors" icon="alert">
        Errors
      </NavLink>
      <NavLink to="/$org/alerts" icon="bell">
        <span className="flex-1">Alerts</span>
        <OpenAlertCount open={useOrgOpenAlerts()} />
      </NavLink>
      <NavLink to="/$org/audit" icon="activity">
        Activity
      </NavLink>
      {admin && (
        <NavLink to="/$org/requests" icon="inbox">
          <span className="flex-1">Requests</span>
          <PendingRequestCount />
        </NavLink>
      )}
      <NavLink to="/$org/settings/members" icon="users">
        Members
      </NavLink>
      {/* Members has its own row: on its page, only that row is current. */}
      <NavLink to="/$org/settings" icon="sliders" exact={onMembers}>
        Organization settings
      </NavLink>
      <NavLink to="/$org/help" icon="help">
        Help
      </NavLink>
      <div className="mx-1 mt-2 mb-1.5 border-t border-line" />
      <Me />
      {operator && (
        <Link to="/console" className="nav-row mt-0.5">
          <Icon name="console" size={14} className="text-warn" />
          <span className="flex-1">Platform console</span>
          <PlatformAlertCount />
        </Link>
      )}
    </>
  );
}

// Badge colours tell organizations apart in the switcher.
const badgeColours = ["bg-fg", "bg-info", "bg-ok", "bg-warn"];

/** An organization's badge: its initial, in a colour of its own. */
function OrgBadge({ org }: { org: Org }) {
  let h = 0;
  for (const c of org.slug) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return (
    // The letter is drawn by CSS, not text: it's decoration, and as text it
    // would become part of the switcher's accessible name.
    <span
      aria-hidden
      data-letter={(org.name || org.slug).charAt(0).toUpperCase()}
      className={`grid size-5 flex-none place-items-center rounded-[5px] text-2xs font-semibold text-side before:content-[attr(data-letter)] ${badgeColours[h % badgeColours.length]}`}
    />
  );
}

/**
 * The organization switcher (ADR 0027): the org the page acts in, your
 * others with your role in each, a new one, and signing out.
 */
function OrgSwitcher() {
  const logout = useLogout();
  const orgs = useOrgs();
  const slug = useOrg();
  const navigate = useNavigate();
  const current = orgs.find((o) => o.slug === slug) ?? orgs[0];
  return (
    <div className="flex items-center gap-1.5 px-1 pt-0.5 pb-2">
      <Menu
        label={`Organization: ${current?.name ?? "none"}`}
        className="min-w-0 flex-1"
        button={
          <span className="flex min-w-0 flex-1 items-center gap-2 rounded-ctl p-1.5 hover:bg-panel">
            {current && <OrgBadge org={current} />}
            <span className="truncate font-semibold">
              {current?.name ?? "No organization"}
            </span>
            <Icon name="chevronDown" size={12} className="text-mut" />
          </span>
        }
      >
        {(close) => (
          <>
            <div
              role="presentation"
              className="px-2.5 pt-1 pb-0.5 text-2xs text-faint"
            >
              Organizations
            </div>
            {orgs.map((o) => (
              <button
                key={o.slug}
                type="button"
                className={menuItem}
                role="menuitem"
                aria-current={o.slug === current?.slug ? "page" : undefined}
                onClick={() => {
                  close();
                  navigate({ to: "/$org", params: { org: o.slug } });
                }}
              >
                <OrgBadge org={o} />
                <span className="min-w-0 flex-1 truncate">{o.name}</span>
                <span className="text-2xs text-faint">{o.role}</span>
                <span className="w-3 text-fg">
                  {o.slug === current?.slug ? "✓" : ""}
                </span>
              </button>
            ))}
            <button
              type="button"
              className={menuItem}
              role="menuitem"
              title="dock org create"
              onClick={() => {
                close();
                openCreate({ kind: "org" });
              }}
            >
              <Icon name="plus" className="text-mut" /> New organization
            </button>
            <hr className="my-1 border-0 border-t border-line" />
            <button
              type="button"
              className={menuItem}
              role="menuitem"
              disabled={logout.isPending}
              onClick={() => logout.mutate()}
            >
              <Icon name="logout" className="text-mut" /> Log out
            </button>
          </>
        )}
      </Menu>
    </div>
  );
}

/**
 * The signed-in person (or token) and what they may do; opens their
 * account (password).
 */
function Me() {
  const session = useSession();
  const logout = useLogout();
  if (!session) return null;
  // The account, and Log out beside it, where people look for it.
  return (
    <div className="flex items-center gap-1">
      <Link
        to="/account"
        title="Your account"
        activeProps={{ "aria-current": "page" }}
        className="nav-row min-w-0 flex-1 gap-2.5 py-1"
      >
        <span className="grid size-6 flex-none place-items-center rounded-full bg-edge text-2xs font-semibold">
          {session.name.slice(0, 1).toUpperCase()}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate">
            {session.email ?? session.name}
          </span>
          <span className="block text-2xs text-faint">
            {session.kind === "token" ? "token, " : ""}
            {session.scope}
            {session.project &&
              ` on ${session.project}${session.environment ? `/${session.environment}` : ""}`}
          </span>
        </span>
      </Link>
      <button
        type="button"
        className="lnk flex-none px-1.5"
        disabled={logout.isPending}
        onClick={() => logout.mutate()}
      >
        Log out
      </button>
    </div>
  );
}

/** How many access requests wait for a decision; nothing when none. */
function PendingRequestCount() {
  const pending = useRequestsIfAdmin().filter((r) => isPending(r)).length;
  if (pending === 0) return null;
  return (
    // A filled count: something waits on the reader.
    <span className="min-w-5 rounded-[5px] bg-warn px-1.5 text-center text-2xs leading-[18px] font-semibold text-side">
      {/* In words, so it reads as requests waiting, not a bare count. */}
      {pending} waiting
    </span>
  );
}

/** The newer release, or that an update runs; nothing when up to date. */
function UpdateBadge() {
  const st = useUpdateNote();
  if (st?.running) return <span className="text-2xs text-prog">updating</span>;
  if (!st?.available) return null;
  return (
    <span className="font-mono text-2xs text-warn">
      {st.latest}
      <span className="sr-only"> available</span>
    </span>
  );
}

/** The platform's open alerts, counted (operators only). */
function PlatformAlertCount() {
  return <OpenAlertCount open={usePlatformOpenAlerts()} />;
}

/** How many alerts are open, in the worst one's colour; nothing when none. */
function OpenAlertCount({ open }: { open: Alert[] }) {
  if (open.length === 0) return null;
  const critical = open.some((a) => a.severity === "critical");
  return (
    // Said in words, so it isn't read as another requests count.
    <span className={`text-2xs ${critical ? "text-fail" : "text-warn"}`}>
      {open.length} {open.length === 1 ? "alert" : "alerts"}
    </span>
  );
}

/**
 * Projects, each with the services of one environment. The current
 * project shows the environment in the URL (a service's, or the project
 * page's #env-<name>); others the one picked in the menu under them, else
 * production. Picking another environment of the current project moves
 * the page there too (useSwitchEnvironment).
 * The current project is open; others open on demand. A project whose
 * services haven't loaded (or failed to) still gets its row. "New
 * project" sits under the last one for those who may create one.
 */
function ProjectTree() {
  const org = useOrg();
  const projects = useProjects();
  const overviews = new Map(
    useProjectTree().map((o) => [o.project.name, o.environments]),
  );
  const here = useCurrentContext();
  const urlEnvironment = useUrlEnvironment();
  const switchEnvironment = useSwitchEnvironment();
  const onEnvPage = useMatches({
    select: (m) => m.at(-1)?.routeId === "/_app/$org/$project/$environment/",
  });
  const session = useSession();
  const newProject = canCreateProject(session) && (
    <button
      type="button"
      onClick={() => openCreate({ kind: "project" })}
      className="flex w-full items-center rounded-ctl text-faint hover:bg-panel hover:text-sec"
    >
      <span className="grid h-[28px] w-[24px] flex-none place-items-center">
        <Icon name="plus" size={12} />
      </span>
      New project
    </button>
  );
  // Folds the reader chose, by project. A project without one is open
  // only while current, so moving on folds what it opened by itself.
  const [folds, setFolds] = useState<ReadonlyMap<string, boolean>>(new Map());
  const setFold = (name: string, expanded: boolean) =>
    setFolds((prev) => new Map(prev).set(name, expanded));
  // The environment picked in a project's menu, by project.
  const [picked, setPicked] = useState<ReadonlyMap<string, string>>(new Map());
  if (projects.length === 0) {
    return (
      <>
        <p className="px-2.5 py-1 text-xs text-faint">No projects yet</p>
        {newProject}
      </>
    );
  }
  return (
    <ul className="flex flex-col gap-0.5">
      {projects.map((project) => {
        const environments = overviews.get(project.name);
        const current = project.name === here.project;
        const expanded = folds.get(project.name) ?? current;
        const choice = (current && urlEnvironment) || picked.get(project.name);
        const env =
          environments?.find((e) => e.environment.name === choice) ||
          environments?.find((e) => e.environment.kind === "production") ||
          environments?.[0];
        return (
          <li key={project.id}>
            <div
              className={`group flex items-center rounded-ctl hover:bg-panel ${current && !here.environment ? "bg-sel" : ""}`}
            >
              <button
                type="button"
                aria-expanded={expanded}
                aria-label={`${expanded ? "Hide" : "Show"} the services of ${project.name}`}
                onClick={() => setFold(project.name, !expanded)}
                className="grid h-[28px] w-[24px] flex-none place-items-center rounded-ctl text-mut hover:text-fg"
              >
                <Icon
                  name={expanded ? "chevronDown" : "chevronRight"}
                  size={12}
                />
              </button>
              <Link
                to="/$org/$project"
                params={{ org, project: project.name }}
                activeOptions={{ exact: true }}
                activeProps={{ "aria-current": "page" }}
                onClick={() => setFold(project.name, true)}
                className={`flex min-w-0 flex-1 items-center gap-2 py-1.5 pr-2 ${current ? "font-medium text-fg" : "text-sec"}`}
              >
                <span className="flex-1 truncate">{project.name}</span>
                {environments && (
                  <ProjectNote
                    services={environments.flatMap((e) => e.services)}
                    shown={env?.services.length ?? 0}
                  />
                )}
              </Link>
              <NewInProject
                project={project.name}
                environment={env?.environment.name}
                current={current}
              />
            </div>
            {expanded && !environments && (
              <p className="py-1 pl-[30px] text-2xs text-faint">
                Services not loaded
              </p>
            )}
            {expanded && env && environments && (
              <ul className="flex flex-col gap-px pb-1">
                {(environments.length > 1 || env.environment.lock) && (
                  <li className="pl-[24px]">
                    <EnvironmentMenu
                      project={project.name}
                      here={current && onEnvPage}
                      shown={env.environment}
                      environments={environments.map((e) => e.environment)}
                      onPick={(name) => {
                        setPicked((prev) =>
                          new Map(prev).set(project.name, name),
                        );
                        if (current) switchEnvironment(name, environments);
                      }}
                    />
                  </li>
                )}
                {env.services.map((s) => (
                  <li key={s.service.id}>
                    <ServiceLink view={s} />
                  </li>
                ))}
                {env.services.length === 0 && (
                  <li className="py-1 pl-[30px] text-2xs text-faint">
                    No services
                  </li>
                )}
              </ul>
            )}
          </li>
        );
      })}
      {newProject && <li>{newProject}</li>}
    </ul>
  );
}

/** The environment the URL is in, a route's $environment. */
function useUrlEnvironment(): string | undefined {
  return useCurrentContext().environment;
}

/**
 * Moves the page to another environment of the current project, on the
 * equivalent page where there is one: the same service tab (a release
 * opens the service, release numbers differ), the same shared variables,
 * else that environment's page. Project pages without an environment
 * (settings, compare) stay where they are.
 */
function useSwitchEnvironment() {
  const navigate = useNavigate();
  const org = useOrg();
  const { project, service, release } = useCurrentContext();
  const leaf = useMatches({ select: (m) => m.at(-1)?.routeId });
  return (environment: string, environments: EnvironmentView[]) => {
    if (!project) return;
    const there = environments.find((e) => e.environment.name === environment);
    const hasService =
      service && there?.services.some((s) => s.service.name === service);
    if (hasService && release) {
      navigate({
        to: "/$org/$project/$environment/$service",
        params: { org, project, environment, service },
      });
    } else if (hasService || leaf?.includes("/settings/variables/")) {
      navigate({
        to: ".",
        params: (prev) => ({ ...prev, environment }),
        search: true,
      });
    } else if (
      service ||
      leaf === "/_app/$org/$project/" ||
      leaf === "/_app/$org/$project/$environment/"
    ) {
      navigate({
        to: "/$org/$project/$environment",
        params: { org, project, environment },
      });
    }
  };
}

/**
 * The environment under a project in the tree, as a small menu: the
 * project's other environments (picking one shows its services here)
 * and Compare.
 */
function EnvironmentMenu({
  project,
  here,
  shown,
  environments,
  onPick,
}: {
  project: string;
  /** On this environment's own page: drawn as the current row. */
  here: boolean;
  shown: Environment;
  environments: Environment[];
  onPick: (name: string) => void;
}) {
  const org = useOrg();
  if (environments.length < 2) {
    return (
      <span className="flex min-h-[24px] items-center gap-1 px-1.5 text-2xs text-mut">
        {shown.name} <LockMark lock={shown.lock} />
      </span>
    );
  }
  // A control, not a label: a box on hover, the chevron always.
  const label = (
    <span
      className={`flex min-h-[24px] items-center gap-1 rounded-ctl border px-1.5 text-xs hover:border-edge hover:bg-panel hover:text-fg ${here ? "border-edge bg-chip text-fg" : "border-line text-sec"}`}
    >
      {shown.name} <LockMark lock={shown.lock} />
      <Icon name="chevronDown" size={12} className="text-mut" />
    </span>
  );
  return (
    <Menu
      label={`Environment shown for ${project}: ${shown.name}`}
      button={label}
    >
      {(close) => (
        <>
          {environments.map((e) => (
            <button
              key={e.id}
              type="button"
              className={menuItem}
              role="menuitem"
              aria-current={e.name === shown.name ? "true" : undefined}
              onClick={() => {
                close();
                onPick(e.name);
              }}
            >
              <span className="flex-1">{e.name}</span>
              <LockMark lock={e.lock} />
              <span className="w-3 text-fg">
                {e.name === shown.name ? "✓" : ""}
              </span>
            </button>
          ))}
          <hr className="my-1 border-0 border-t border-line" />
          <Link
            to="/$org/$project/compare"
            params={{ org, project }}
            className={menuItem}
            role="menuitem"
            onClick={close}
          >
            Compare environments
          </Link>
        </>
      )}
    </Menu>
  );
}

/**
 * "Archived" under the project tree: a fold listing archived projects
 * (`dock project list --all`), fetched once opened. It opens by itself on
 * an archived project's page.
 */
function ArchivedProjects() {
  const org = useOrg();
  const here = useCurrentContext().project;
  const onArchived =
    useQuery({
      ...queries.overview(here ?? ""),
      enabled: !!here,
      select: (o) => !!o.project.archived_at,
    }).data ?? false;
  const [open, setOpen] = useState<boolean>();
  const shown = open ?? onArchived;
  const archived = useQuery({
    ...queries.archivedProjects(),
    enabled: shown,
  });
  return (
    <>
      <button
        type="button"
        aria-expanded={shown}
        onClick={() => setOpen(!shown)}
        className="flex w-full items-center rounded-ctl text-faint hover:bg-panel hover:text-sec"
      >
        <span className="grid h-[28px] w-[24px] flex-none place-items-center">
          <Icon name={shown ? "chevronDown" : "chevronRight"} size={12} />
        </span>
        Archived
      </button>
      {shown && archived.error && (
        <p className="pl-[30px] text-2xs">
          <ErrorText error={archived.error} />
        </p>
      )}
      {shown && archived.data?.length === 0 && (
        <p className="py-1 pl-[30px] text-2xs text-faint">None</p>
      )}
      {shown && !!archived.data?.length && (
        <ul className="flex flex-col gap-px pb-1">
          {archived.data.map((p) => (
            <li key={p.id}>
              <Link
                to="/$org/$project"
                params={{ org, project: p.name }}
                activeOptions={{ exact: true }}
                activeProps={{ "aria-current": "page" }}
                className="nav-row py-[5px] pl-[30px] text-faint"
              >
                <span className="flex-1 truncate">{p.name}</span>
                <span className="text-2xs" title={p.archived_at}>
                  archived {since(p.archived_at)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/**
 * The + on a project row: a new service in the environment the tree
 * shows, or a new environment. Always shown on the current project;
 * others show it on hover or focus on wide screens.
 */
function NewInProject({
  project,
  environment,
  current,
}: {
  project: string;
  environment: string | undefined;
  /** The project the page is in: its + always shows. */
  current: boolean;
}) {
  const session = useSession();
  const env = environment && { project, environment };
  const service = env && canDeploy(session, env) ? env : undefined;
  const newEnv = canCreateEnvironment(session, project);
  if (!service && !newEnv) return null;
  return (
    <Menu
      label={`New in ${project}`}
      right
      className="flex-none"
      button={
        <span
          className={`grid h-[28px] w-[24px] place-items-center text-faint group-focus-within:opacity-100 group-hover:opacity-100 hover:text-fg in-aria-expanded:opacity-100 ${current ? "" : "lg:opacity-0"}`}
        >
          <Icon name="plus" size={12} />
        </span>
      }
    >
      {(close) => (
        <>
          {service && (
            <button
              type="button"
              className={menuItem}
              role="menuitem"
              onClick={() => {
                close();
                openCreate({ kind: "service", env: service });
              }}
            >
              New service in {service.environment}
            </button>
          )}
          {newEnv && (
            <button
              type="button"
              className={menuItem}
              role="menuitem"
              onClick={() => {
                close();
                openCreate({ kind: "environment", project });
              }}
            >
              New environment
            </button>
          )}
        </>
      )}
    </Menu>
  );
}

/**
 * "deploying" or "1 failed" in any environment, else how many services
 * the tree lists (`shown`: the environment it shows).
 */
function ProjectNote({
  services,
  shown,
}: {
  services: ServiceView[];
  shown: number;
}) {
  const failed = services.filter((s) => serviceState(s).tone === "fail");
  if (services.some((s) => s.deploying)) {
    return <span className="text-2xs text-warn">deploying</span>;
  }
  if (failed.length > 0) {
    return <span className="text-2xs text-fail">{failed.length} failed</span>;
  }
  return <span className="text-2xs text-faint">{shown}</span>;
}

function ServiceLink({ view }: { view: ServiceView }) {
  const org = useOrg();
  const state = serviceState(view);
  const { service } = view;
  // Only what needs a look: a healthy service says nothing.
  const note =
    service.staged === "archive" && !view.deploying
      ? "archive waits for apply"
      : state.tone === "ok"
        ? undefined
        : view.deploying
          ? "deploying"
          : state.label;
  return (
    <Link
      to="/$org/$project/$environment/$service"
      params={{ org, ...serviceRef(service) }}
      activeProps={{ "aria-current": "page" }}
      className="nav-row py-[5px] pl-[30px]"
      title={state.label}
    >
      {state.glyph ? (
        <span className={toneText[state.tone]}>
          <Glyph char={state.glyph} />
        </span>
      ) : (
        // A staged new service runs nothing: no dot, the space for one.
        <span aria-hidden className="w-[7px] flex-none" />
      )}
      <span className="flex-1 truncate">{service.name}</span>
      <span
        className={`max-w-24 truncate text-2xs ${state.tone === "ok" || state.tone === "mut" ? "text-faint" : toneText[state.tone]}`}
      >
        {note}
      </span>
    </Link>
  );
}

/**
 * The platform console's sidebar: infrastructure (servers, each with its
 * link status), access, and operations. Only admins reach it.
 */
function ConsoleNav() {
  const servers = useServers();
  return (
    <>
      {/* In place of the organization: the console has none (ADR 0062). */}
      <div className="px-2.5 pt-1 pb-2 font-semibold">Platform</div>
      <NavLink to="/console" exact icon="grid">
        Overview
      </NavLink>
      <NavLink to="/console/orgs" icon="users">
        Organizations
      </NavLink>
      <div className={groupLabel}>Infrastructure</div>
      <NavLink to="/console/servers" exact icon="server">
        Servers
      </NavLink>
      {servers.map((s) => {
        const state = serverState(s.status);
        return (
          <Link
            key={s.id}
            to="/console/servers/$server"
            params={{ server: s.name }}
            activeProps={{ "aria-current": "page" }}
            className="nav-row py-[5px] pl-[30px]"
          >
            <span className={toneText[state.tone]}>
              <Glyph char={state.glyph ?? "●"} />
            </span>
            <span className="flex-1 truncate font-mono text-xs">{s.name}</span>
            {s.status !== "online" && (
              <span className={`text-2xs ${toneText[state.tone]}`}>
                {state.label}
              </span>
            )}
          </Link>
        );
      })}
      <NavLink to="/console/registry" icon="box">
        Registry
      </NavLink>
      <NavLink to="/console/backups" icon="history">
        Backups
      </NavLink>
      <NavLink to="/console/storage" icon="database">
        Storage
      </NavLink>
      <div className={groupLabel}>Access</div>
      <NavLink to="/console/users" icon="users">
        Users
      </NavLink>
      <NavLink to="/console/data-access" icon="shield">
        Data access
      </NavLink>
      <div className={groupLabel}>Operations</div>
      <NavLink to="/console/alerts" icon="bell">
        <span className="flex-1">Alerts</span>
        <PlatformAlertCount />
      </NavLink>
      <NavLink to="/console/audit" icon="activity">
        Activity
      </NavLink>
      <div className={groupLabel}>System</div>
      <NavLink to="/console/address" icon="globe">
        Address
      </NavLink>
      <NavLink to="/console/github" icon="graph">
        GitHub
      </NavLink>
      <NavLink to="/console/cloudflare" icon="cloud">
        Cloudflare
      </NavLink>
      <NavLink to="/console/analytics" icon="bars">
        Analytics
      </NavLink>
      <NavLink to="/console/updates" icon="upload">
        <span className="flex-1">Updates</span>
        <UpdateBadge />
      </NavLink>
      <div className="min-h-6 flex-1" />
      <Me />
    </>
  );
}
