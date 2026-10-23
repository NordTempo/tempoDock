import {
  Link,
  type LinkProps,
  useMatches,
  useRouterState,
} from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useLogout } from "../data/mutations";
import {
  canDeploy,
  isDatabase,
  serviceRef,
  useIsAdmin,
  useProjectTree,
  useServers,
  useSession,
} from "../data/queries";
import type { ServiceView } from "../data/types";
import { useOpenAlertsIfAdmin } from "../features/alerts/data";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import type { FileRoutesById } from "../routeTree.gen";
import { since } from "../ui/format";
import { Icon, type IconName } from "../ui/icons";
import { Glyph, Menu, menuItem } from "../ui/kit";
import { serverState, serviceState, toneText } from "../ui/status";
import { modKey } from "./keys";
import { useCurrentContext } from "./location";
import { usePalette } from "./palette";

/**
 * Which sidebar a page gets: the project tree, workspace settings, or the
 * platform console (operators: servers).
 */
export type ShellMode = "app" | "settings" | "console";

const modes: Partial<Record<keyof FileRoutesById, ShellMode>> = {
  "/_app/users": "settings",
  "/_app/tokens": "settings",
  "/_app/data-policy": "settings",
  "/_app/alerts": "settings",
  "/_app/audit": "settings",
  "/_app/servers": "console",
  "/_app/servers_/$server": "console",
};

export const useShellMode = (): ShellMode =>
  useMatches({
    select: (matches) => {
      const id = matches.at(-1)?.routeId;
      return (id && modes[id]) || "app";
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
            : "Workspace"
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
  const admin = useIsAdmin();
  return (
    <>
      <Workspace />
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
      <div className="min-h-6 flex-1" />
      <NavLink to="/audit" icon="activity">
        Activity
      </NavLink>
      {admin ? (
        <NavLink to="/users" icon="users">
          Members &amp; tokens
        </NavLink>
      ) : (
        <NavLink to="/tokens" icon="key">
          Tokens
        </NavLink>
      )}
      {admin && (
        <NavLink to="/alerts" icon="bell">
          <span className="flex-1">Alerts</span>
          <OpenAlertCount />
        </NavLink>
      )}
      <div className="mx-1 mt-2 mb-1.5 border-t border-line" />
      <Me />
      {admin && (
        <Link
          to="/servers"
          className="mx-1 mt-1.5 flex items-center gap-2 rounded-ctl border border-dashed border-dash px-2 py-1.5 text-xs text-warn hover:bg-warn-bg"
        >
          <Icon name="console" size={13} />
          <span className="flex-1">Platform console</span>
          <span className="text-2xs text-faint">servers</span>
        </Link>
      )}
    </>
  );
}

/**
 * The workspace this Dock is. One Dock is one workspace today, so the menu
 * holds it and signing out.
 */
function Workspace() {
  const logout = useLogout();
  const host = window.location.hostname;
  const badge = (
    <span className="grid size-5 flex-none place-items-center rounded-[5px] bg-fg text-2xs font-semibold text-side">
      D
    </span>
  );
  return (
    <div className="flex items-center gap-1.5 px-1 pt-0.5 pb-2">
      <Menu
        label="Workspace"
        className="min-w-0 flex-1"
        button={
          <span className="flex min-w-0 flex-1 items-center gap-2 rounded-ctl p-1.5 hover:bg-panel">
            {badge}
            <span className="truncate font-semibold">{host}</span>
            <Icon name="chevronDown" size={12} className="text-mut" />
          </span>
        }
      >
        {() => (
          <>
            <span className={menuItem} aria-current="page">
              {badge} {host}
            </span>
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

/** The signed-in person (or token) and what they may do. */
function Me() {
  const session = useSession();
  if (!session) return null;
  return (
    <div className="flex items-center gap-2.5 px-2 py-1">
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
    </div>
  );
}

/** How many alerts are open, in the worst one's colour; nothing when none. */
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
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (name: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(name)) next.add(name);
      return next;
    });
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
        const expanded = current !== open.has(project.name);
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
                onClick={() => toggle(project.name)}
                className="grid h-7 w-6 flex-none place-items-center rounded-ctl text-mut hover:text-fg"
              >
                <Icon
                  name={expanded ? "chevronDown" : "chevronRight"}
                  size={12}
                />
              </button>
              <Link
                to="/$project"
                params={{ project: project.name }}
                activeOptions={{ exact: true }}
                activeProps={{ "aria-current": "page" }}
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
      to="/$project/$environment/$service"
      params={serviceRef(service)}
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
  const admin = useIsAdmin();
  return (
    <>
      <Link to="/" className="nav-row mb-1 text-mut">
        <Icon name="chevronLeft" /> Back to projects
      </Link>
      <div className={groupLabel}>Workspace</div>
      {admin && <NavLink to="/users">Members</NavLink>}
      <NavLink to="/tokens">Tokens</NavLink>
      {admin && <NavLink to="/data-policy">Data access</NavLink>}
      {admin && (
        <NavLink to="/alerts">
          <span className="flex-1">Alerts</span>
          <OpenAlertCount />
        </NavLink>
      )}
      <NavLink to="/audit">Audit log</NavLink>
      <div className="min-h-6 flex-1" />
      <Me />
    </>
  );
}

/** The operator side: servers, with each one's link status. */
function ConsoleNav() {
  const servers = useServers();
  const admin = useIsAdmin();
  return (
    <>
      <div className="px-2.5 pt-1 pb-2">
        <span className="font-semibold">dock</span>{" "}
        <span className="text-faint">· platform</span>
      </div>
      <div className={groupLabel}>Infrastructure</div>
      <NavLink to="/servers" exact icon="server">
        Servers
      </NavLink>
      {servers.map((s) => {
        const state = serverState(s.status);
        return admin ? (
          <Link
            key={s.id}
            to="/servers/$server"
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
        ) : null;
      })}
      <div className="min-h-6 flex-1" />
      <Me />
    </>
  );
}
