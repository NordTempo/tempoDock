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
  isDatabase,
  queries,
  serviceRef,
  useIsAdmin,
  useIsOperator,
  useOrgs,
  useProjectTree,
  useServers,
  useSession,
} from "../data/queries";
import type { Org, ServiceView } from "../data/types";
import { useOpenAlertsIfAdmin } from "../features/alerts/data";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import { useUpdateNote } from "../features/update/data";
import type { FileRoutesById } from "../routeTree.gen";
import { since } from "../ui/format";
import { Icon, type IconName } from "../ui/icons";
import { ErrorText, Glyph, Menu, menuItem } from "../ui/kit";
import { serverState, serviceState, toneText } from "../ui/status";
import { modKey } from "./keys";
import { useCurrentContext, useOrg } from "./location";
import { usePalette } from "./palette";

/**
 * Which sidebar a page gets: the project tree, organization settings (its
 * tokens and activity, your account), or the platform console (operators:
 * every page under /console).
 */
export type ShellMode = "app" | "settings" | "console";

const settings = new Set<keyof FileRoutesById>([
  "/_app/$org/settings/members",
  "/_app/$org/settings/variables",
  "/_app/$org/settings/general",
  "/_app/$org/tokens",
  "/_app/$org/audit",
  "/_app/account",
]);

export const useShellMode = (): ShellMode =>
  useMatches({
    select: (matches) => {
      if (matches.some((m) => m.routeId === "/_app/console")) return "console";
      const id = matches.at(-1)?.routeId;
      return id && settings.has(id) ? "settings" : "app";
    },
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
      aria-label={
        mode === "console"
          ? "Platform"
          : mode === "settings"
            ? "Settings"
            : "Organization"
      }
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
        className={`${open ? "flex" : "hidden"} flex-1 flex-col gap-0.5 lg:flex`}
      >
        {mode === "app" && <AppNav />}
        {mode === "settings" && <SettingsNav />}
        {mode === "console" && <ConsoleNav />}
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
  const operator = useIsOperator();
  return (
    <>
      <OrgSwitcher />
      <button
        type="button"
        onClick={() => openPalette()}
        className="mx-1 mb-2.5 flex items-center gap-2 rounded-[7px] border border-line bg-panel px-2.5 py-[7px] text-left text-mut hover:border-edge hover:text-sec"
      >
        <Icon name="search" />
        <span className="flex-1">Jump to…</span>
        <kbd>{modKey.trim()}K</kbd>
      </button>
      <div className="px-2.5 pt-1.5 pb-1 text-2xs font-medium text-faint">
        Projects
      </div>
      <ProjectTree />
      <ArchivedProjects />
      <div className="min-h-6 flex-1" />
      <NavLink to="/$org/audit" icon="activity">
        Activity
      </NavLink>
      {!operator && (
        <NavLink to="/$org/tokens" icon="key">
          Tokens
        </NavLink>
      )}
      <div className="mx-1 mt-2 mb-1.5 border-t border-line" />
      <Me />
      {operator && (
        <Link
          to="/console"
          className="mx-1 mt-1.5 flex items-center gap-2 rounded-ctl border border-dashed border-dash px-2 py-1.5 text-xs text-warn hover:bg-warn-bg"
        >
          <Icon name="console" size={13} />
          <span className="flex-1">Platform console</span>
          <OpenAlertCount />
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
    <span
      className={`grid size-5 flex-none place-items-center rounded-[5px] text-2xs font-semibold text-side ${badgeColours[h % badgeColours.length]}`}
    >
      {(org.name || org.slug).charAt(0).toUpperCase()}
    </span>
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
        label="Organization"
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
            <div className="px-2.5 pt-1 pb-0.5 text-2xs text-faint">
              Organizations
            </div>
            {orgs.map((o) => (
              <button
                key={o.slug}
                type="button"
                className={menuItem}
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
              title="dock org create"
              onClick={() => {
                close();
                openCreate({ kind: "org" });
              }}
            >
              <Icon name="plus" className="text-mut" /> New organization
            </button>
            {current && (
              <Link
                to="/$org/settings"
                params={{ org: current.slug }}
                className={menuItem}
                onClick={close}
              >
                <Icon name="sliders" className="text-mut" /> Organization
                settings
              </Link>
            )}
            <div className="my-1 border-t border-line" />
            <button
              type="button"
              className={menuItem}
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
  if (!session) return null;
  return (
    <Link
      to="/account"
      title="Your account"
      activeProps={{ "aria-current": "page" }}
      className="nav-row gap-2.5 py-1"
    >
      <span className="grid size-6 flex-none place-items-center rounded-full bg-edge text-2xs font-semibold">
        {session.name.slice(0, 1).toUpperCase()}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate">{session.email ?? session.name}</span>
        <span className="block text-2xs text-faint">
          {session.kind === "token" ? "token, " : ""}
          {session.scope}
          {session.project &&
            ` on ${session.project}${session.environment ? `/${session.environment}` : ""}`}
        </span>
      </span>
    </Link>
  );
}

/** How many alerts are open, in the worst one's colour; nothing when none. */
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

function OpenAlertCount() {
  const open = useOpenAlertsIfAdmin();
  if (open.length === 0) return null;
  const critical = open.some((a) => a.severity === "critical");
  return (
    <span className={`text-2xs ${critical ? "text-fail" : "text-warn"}`}>
      {open.length}
      <span className="sr-only"> open</span>
    </span>
  );
}

/**
 * Projects, each with the services of one environment: the one in view,
 * else production. The current project is open; others open on demand.
 * "New project" sits under the last one for those who may create one.
 */
function ProjectTree() {
  const org = useOrg();
  const tree = useProjectTree();
  const here = useCurrentContext();
  const session = useSession();
  const newProject = canCreateProject(session) && (
    <button
      type="button"
      onClick={() => openCreate({ kind: "project" })}
      className="flex w-full items-center rounded-ctl text-faint hover:bg-panel hover:text-sec"
    >
      <span className="grid h-7 w-6 flex-none place-items-center">
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
  if (tree.length === 0) {
    return (
      <>
        <p className="px-2.5 py-1 text-xs text-faint">No projects yet</p>
        {newProject}
      </>
    );
  }
  return (
    <ul className="flex flex-col gap-0.5">
      {tree.map(({ project, environments }) => {
        const current = project.name === here.project;
        const expanded = folds.get(project.name) ?? current;
        const env =
          (current &&
            environments.find(
              (e) => e.environment.name === here.environment,
            )) ||
          environments.find((e) => e.environment.kind === "production") ||
          environments[0];
        const all = environments.flatMap((e) => e.services);
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
                className="grid h-7 w-6 flex-none place-items-center rounded-ctl text-mut hover:text-fg"
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
                <ProjectNote services={all} />
              </Link>
              <NewInProject
                project={project.name}
                environment={env?.environment.name}
              />
            </div>
            {expanded && env && (
              <ul className="flex flex-col gap-px pb-1">
                {environments.length > 1 && (
                  <li className="truncate pt-0.5 pb-0.5 pl-[30px] text-2xs text-faint">
                    {env.environment.name}
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
        <span className="grid h-7 w-6 flex-none place-items-center">
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
 * shows, or a new environment. Shown on hover or focus on wide screens.
 */
function NewInProject({
  project,
  environment,
}: {
  project: string;
  environment: string | undefined;
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
        <span className="grid h-7 w-6 place-items-center text-faint group-focus-within:opacity-100 group-hover:opacity-100 hover:text-fg in-aria-expanded:opacity-100 lg:opacity-0">
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

/** "deploying", "1 failed", or how many services. */
function ProjectNote({ services }: { services: ServiceView[] }) {
  const failed = services.filter((s) => serviceState(s).tone === "fail");
  if (services.some((s) => s.deploying)) {
    return <span className="text-2xs text-warn">deploying</span>;
  }
  if (failed.length > 0) {
    return <span className="text-2xs text-fail">{failed.length} failed</span>;
  }
  return <span className="text-2xs text-faint">{services.length}</span>;
}

function ServiceLink({ view }: { view: ServiceView }) {
  const org = useOrg();
  const state = serviceState(view);
  const { service } = view;
  const note =
    state.tone === "ok"
      ? isDatabase(service)
        ? "db"
        : service.status.updated_at && since(service.status.updated_at)
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
      <span className={toneText[state.tone]}>
        <Glyph char={state.glyph ?? "○"} />
      </span>
      <span className="flex-1 truncate">{service.name}</span>
      <span
        className={`max-w-24 truncate text-2xs ${state.tone === "ok" || state.tone === "mut" ? "text-faint" : toneText[state.tone]}`}
      >
        {note}
      </span>
    </Link>
  );
}

function SettingsNav() {
  const operator = useIsOperator();
  const admin = useIsAdmin();
  const slug = useOrg();
  const org = useOrgs().find((o) => o.slug === slug);
  const params = { org: slug };
  return (
    <>
      <Link to="/" className="nav-row mb-1 text-mut">
        <Icon name="chevronLeft" /> Back to projects
      </Link>
      <div className={groupLabel}>{org?.name ?? "Organization"}</div>
      {admin && (
        <NavLink to="/$org/settings/members" params={params}>
          Members
        </NavLink>
      )}
      <NavLink to="/$org/settings/variables" params={params}>
        Variables
      </NavLink>
      {!operator && (
        <NavLink to="/$org/tokens" params={params}>
          Tokens
        </NavLink>
      )}
      <NavLink to="/$org/audit" params={params}>
        Activity
      </NavLink>
      <NavLink to="/$org/settings/general" params={params}>
        General
      </NavLink>
      <div className="min-h-6 flex-1" />
      <Me />
    </>
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
      <div className="px-2.5 pt-1 pb-2">
        <span className="font-semibold">dock</span>{" "}
        <span className="text-faint">· platform</span>
      </div>
      <NavLink to="/console" exact icon="grid">
        Overview
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
      <NavLink to="/console/tokens" icon="key">
        Tokens
      </NavLink>
      <NavLink to="/console/data-access" icon="shield">
        Data access
      </NavLink>
      <div className={groupLabel}>Operations</div>
      <NavLink to="/console/alerts" icon="bell">
        <span className="flex-1">Alerts</span>
        <OpenAlertCount />
      </NavLink>
      <NavLink to="/console/audit" icon="activity">
        Audit
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
      <NavLink to="/console/updates" icon="upload">
        <span className="flex-1">Updates</span>
        <UpdateBadge />
      </NavLink>
      <div className="min-h-6 flex-1" />
      <Me />
    </>
  );
}
