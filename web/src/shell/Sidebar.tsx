import {
  Link,
  useMatches,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useLogout } from "../data/mutations";
import {
  canDeploy,
  serviceRef,
  useOrgs,
  useProjects,
  useProjectTree,
  useSeesPlatform,
  useSession,
} from "../data/queries";
import type { Org, ServiceView } from "../data/types";
import {
  type Alert,
  useOrgOpenAlerts,
  usePlatformOpenAlerts,
} from "../features/alerts/data";
import {
  type ConsoleEntry,
  consoleEntryOf,
  consoleNav,
} from "../features/console/nav";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import { closeHelp, openHelp, useHelpOpen } from "../features/help/open";
import { useUpdateNote } from "../features/update/data";
import { plural } from "../ui/format";
import { Icon } from "../ui/icons";
import { Glyph, Menu, menuItem } from "../ui/kit";
import { serviceState, toneText } from "../ui/status";
import { modKey, useShortcuts } from "./keys";
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
 * The left column: the project tree (or its rail) on the Projects page
 * and inside a project, the console's views on the console. The
 * organization's other views have none (useProjectsColumn; the shell
 * leaves it out there). Wide screens keep it in view while the page
 * scrolls; narrow ones put it above the page.
 */
export function Sidebar({ mode }: { mode: ShellMode }) {
  // Narrow screens fold the sidebar behind a button; moving on folds it.
  const [open, setOpen] = useState(false);
  const [tree, toggleTree] = useTreeOpen();
  useShortcuts(
    mode === "app"
      ? [{ key: "[", label: "project tree or rail", run: toggleTree }]
      : [],
  );
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [foldedAt, setFoldedAt] = useState(path);
  if (foldedAt !== path) {
    setFoldedAt(path);
    setOpen(false);
  }
  return (
    <nav
      aria-label={mode === "console" ? "Platform" : "Organization"}
      className={columnClass(mode === "console" || tree)}
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
        {mode === "app" ? (
          <AppNav tree={tree} onToggle={toggleTree} />
        ) : (
          <ConsoleNav />
        )}
      </div>
    </nav>
  );
}

/**
 * The left column's box: the tree's 248px or the rail's 60px on wide
 * screens, a strip above the page on narrow ones. The shell's loading
 * frame (ShellPending) uses it too, so the column doesn't move on load.
 */
export const columnClass = (wide: boolean) =>
  `flex flex-col gap-0.5 border-b border-line bg-side px-2 py-2.5 lg:sticky ${wide ? "lg:w-[248px]" : "lg:w-[60px]"} lg:flex-none lg:overflow-y-auto lg:border-r lg:border-b-0 lg:top-(--shell-top) lg:h-[calc(100vh-var(--shell-top))]`;

/**
 * Whether the page is in Projects: the Projects page (`/$org`) or any
 * page inside a project (its services, environments, settings). Those
 * have the project tree or rail down their left side and Projects marked
 * in the top bar; the organization's other views (Errors, Analytics,
 * Images, Status pages, Inbox, Settings) get the full width (Saile).
 */
export const useProjectsColumn = () =>
  useMatches({
    select: (ms) =>
      ms.some(
        (m) =>
          m.routeId === "/_app/$org/" ||
          m.routeId.startsWith("/_app/$org/$project"),
      ),
  });

/**
 * Whether the left column shows the full project tree or only the project
 * rail (Saile's pick, option 3): an explicit toggle, kept per browser
 * until changed. `[` toggles it.
 */
const treeKey = "dock.tree";
export function useTreeOpen() {
  const [open, setOpen] = useState(
    () => localStorage.getItem(treeKey) !== "rail",
  );
  const toggle = () =>
    setOpen((o) => {
      localStorage.setItem(treeKey, o ? "rail" : "tree");
      return !o;
    });
  return [open, toggle] as const;
}

/**
 * The org's left column (option 3): the project tree, or the rail of
 * project tiles; the org's views are in the top bar (TopBar).
 */
function AppNav({ tree, onToggle }: { tree: boolean; onToggle: () => void }) {
  return (
    <>
      {/* Expanded stays expanded until collapsed again (Saile). */}
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={tree}
        aria-label={tree ? "Collapse the projects" : "Expand the projects"}
        title={`${tree ? "Collapse to the project rail" : "Expand to the project tree"} ([)`}
        className={`nav-row text-mut ${tree ? "" : "justify-center px-0"}`}
      >
        {tree ? (
          <>
            <span className="flex-1 text-left text-2xs font-medium text-faint">
              Projects
            </span>
            <Icon name="chevronLeft" size={14} />
            <kbd>[</kbd>
          </>
        ) : (
          <Icon name="chevronRight" size={14} />
        )}
      </button>
      {tree ? <ProjectTree /> : <ProjectRail />}
    </>
  );
}

/**
 * The collapsed column: a tile per project, its initials and a dot for
 * its health, worst of its services (failing red, needs a look amber,
 * running green; none while nothing is deployed). The current project's
 * tile is marked.
 */
function ProjectRail() {
  const org = useOrg();
  const projects = useProjects();
  const tone = new Map(
    useProjectTree().map((o) => {
      const tones = o.environments.flatMap((e) =>
        e.services.map((v) => serviceState(v).tone),
      );
      return [
        o.project.name,
        tones.includes("fail")
          ? "fail"
          : tones.includes("warn")
            ? "warn"
            : tones.includes("ok")
              ? "ok"
              : undefined,
      ] as const;
    }),
  );
  const dot = { fail: "bg-fail", warn: "bg-warn", ok: "bg-ok" } as const;
  const word = {
    fail: "failing",
    warn: "needs a look",
    ok: "running",
  } as const;
  return (
    <ul className="flex flex-col items-center gap-1">
      {projects.map((p) => {
        const t = tone.get(p.name);
        return (
          <li key={p.name}>
            <Link
              to="/$org/$project"
              params={{ org, project: p.name }}
              activeProps={{ "aria-current": "page" }}
              title={t ? `${p.name}: ${word[t]}` : p.name}
              className="relative grid size-9 place-items-center rounded-ctl border border-line text-xs font-medium uppercase hover:bg-hover aria-[current=page]:border-edge aria-[current=page]:bg-sel"
            >
              {p.name.slice(0, 2)}
              {t && (
                <span
                  role="img"
                  aria-label={`, ${word[t]}`}
                  className={`absolute -top-0.5 -right-0.5 size-2 rounded-full ${dot[t]}`}
                />
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * The org's top bar (option 3): the switcher, the org's views,
 * search, Inbox, Settings and the account. Members and Activity live
 * under Settings; Help (?) here and in the account menu; the console in
 * the switcher.
 */
export function TopBar() {
  const openPalette = usePalette();
  return (
    // Sticky over every page in the shell's top stack; its height is
    // --shell-top (index.css).
    <header className={topBarClass}>
      <OrgSwitcher />
      <nav
        aria-label="Organization views"
        className="hidden items-center gap-0.5 md:flex"
      >
        {orgViews.map(([to, label]) => (
          <TopLink key={to} to={to}>
            {label}
          </TopLink>
        ))}
      </nav>
      {/* Narrow windows: the same views in a menu. */}
      <ViewsMenu />
      <span className="flex-1" />
      <button
        type="button"
        onClick={() => openPalette()}
        className="flex min-h-[28px] items-center gap-2 rounded-ctl border border-line px-2.5 text-mut hover:border-edge hover:text-sec"
      >
        <Icon name="search" />
        {/* Named by its text (WCAG 2.5.3); on narrow screens the word is only read. */}
        <span className="sr-only sm:not-sr-only">Search</span>
        <kbd className="hidden sm:inline">{modKey.trim()}K</kbd>
      </button>
      <TopLink to="/$org/inbox">
        Inbox <InboxCount />
      </TopLink>
      {/* "Settings", the word every level uses for its last tab: a
          project's and a service's are tabs on their pages, so this one is
          the organization's (members, alerts, connections). */}
      <TopLink to="/$org/settings">Settings</TopLink>
      <HelpButton />
      <AccountMenu />
    </header>
  );
}

/**
 * The top bar's box and its links' look, shared with the shell's loading
 * frame (ShellPending) so the bar doesn't move when the shell arrives.
 */
export const topBarClass =
  "app-shell flex h-[52px] items-center gap-1 border-b border-line bg-side px-3 whitespace-nowrap";
export const topLinkClass =
  "flex min-h-[28px] items-center gap-1.5 rounded-ctl px-2 text-mut hover:text-fg aria-[current=page]:bg-sel aria-[current=page]:text-fg sm:px-2.5";

/** The org's views, in the top bar's order. */
export const orgViews = [
  ["/$org", "Projects"],
  ["/$org/errors", "Errors"],
  ["/$org/analytics", "Analytics"],
  ["/$org/images", "Images"],
  ["/$org/status-pages", "Status pages"],
] as const;

/** The top bar's views below md: one menu, named after the current view. */
function ViewsMenu() {
  const org = useOrg();
  const navigate = useNavigate();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const here =
    [...orgViews]
      .reverse()
      .find(([to]) => path.startsWith(to.replace("$org", org)))?.[1] ?? "Views";
  return (
    <span className="md:hidden">
      <Menu
        label="Organization views"
        button={
          <span className="flex min-h-[28px] items-center gap-1.5 rounded-ctl px-2 text-fg">
            {here} <Icon name="chevronDown" size={12} className="text-mut" />
          </span>
        }
      >
        {(close) =>
          orgViews.map(([to, label]) => (
            <button
              key={to}
              type="button"
              role="menuitem"
              className={menuItem}
              onClick={() => {
                close();
                navigate({ to, params: { org } });
              }}
            >
              {label}
            </button>
          ))
        }
      </Menu>
    </span>
  );
}

/** A link in the top bar; current on its page (Projects only exactly). */
function TopLink({
  to,
  children,
}: {
  to: (typeof orgViews)[number][0] | "/$org/inbox" | "/$org/settings";
  children: ReactNode;
}) {
  const org = useOrg();
  // Projects stays marked inside a project, not only on its list.
  const inProjects = useProjectsColumn();
  return (
    <Link
      to={to}
      params={{ org }}
      activeOptions={{ exact: to === "/$org" }}
      {...(to === "/$org" && inProjects && { "aria-current": "page" as const })}
      className={topLinkClass}
    >
      {children}
    </Link>
  );
}

/** The top bar's ?: opens or closes help for the page (features/help). */
function HelpButton() {
  const open = useHelpOpen();
  return (
    <button
      type="button"
      aria-label="Help"
      aria-expanded={open}
      title="Help (?)"
      onClick={() => (open ? closeHelp() : openHelp())}
      // Phones reach Help from the account menu, so the bar fits 360px.
      className="hidden min-h-[28px] items-center rounded-ctl px-2.5 text-mut hover:text-fg aria-expanded:bg-sel aria-expanded:text-fg sm:flex"
    >
      <span aria-hidden>?</span>
    </button>
  );
}

// Badge colours tell organizations apart in the switcher.
const badgeColours = ["bg-fg", "bg-info", "bg-ok", "bg-warn"];

/**
 * An organization's badge: its initial, in a colour of its own. Only the
 * slug and name are read, so the loading frame draws it from the address.
 */
export function OrgBadge({ org }: { org: Pick<Org, "slug" | "name"> }) {
  let h = 0;
  for (const c of org.slug) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return (
    // The letter is drawn by CSS, not text: it's decoration, and as text it
    // would become part of the switcher's accessible name.
    <span
      aria-hidden
      data-letter={(org.name || org.slug).charAt(0).toUpperCase()}
      className={`grid size-5 flex-none place-items-center rounded-ctl text-2xs font-semibold text-side before:content-[attr(data-letter)] ${badgeColours[h % badgeColours.length]}`}
    />
  );
}

/**
 * The organization switcher (ADR 0027): the org the page acts in, your
 * others with your role in each, a new one, and signing out.
 */
function OrgSwitcher() {
  const operator = useSeesPlatform();
  const orgs = useOrgs();
  const slug = useOrg();
  const navigate = useNavigate();
  const current = orgs.find((o) => o.slug === slug) ?? orgs[0];
  return (
    <div className="flex items-center gap-1.5">
      <Menu
        label={`Organization: ${current?.name ?? "none"}`}
        className="min-w-0 flex-1"
        button={
          <span className="flex min-w-0 flex-1 items-center gap-2 rounded-ctl p-1.5 hover:bg-panel">
            {current && <OrgBadge org={current} />}
            {/* Phones show the badge alone; the button's label names it. */}
            <span className="hidden truncate font-semibold sm:inline">
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
            {operator && (
              <>
                <hr className="my-1 border-0 border-t border-line" />
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  onClick={() => {
                    close();
                    navigate({ to: "/console" });
                  }}
                >
                  <Icon name="console" className="text-warn" /> Platform console
                  <span className="ml-auto">
                    <PlatformAlertCount />
                  </span>
                </button>
              </>
            )}
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

/**
 * The top bar's account menu: who is signed in, the account page, Help
 * and Log out (option 3).
 */
function AccountMenu() {
  const session = useSession();
  const logout = useLogout();
  const navigate = useNavigate();
  if (!session) return null;
  return (
    <Menu
      label="Account"
      right
      button={
        <span
          className="grid size-7 place-items-center rounded-full bg-edge text-2xs font-semibold"
          title={session.email ?? session.name}
        >
          {session.name.slice(0, 1).toUpperCase()}
        </span>
      }
    >
      {(close) => (
        <>
          <div role="presentation" className="px-2.5 pt-1 pb-1.5">
            <span className="block truncate">
              {session.email ?? session.name}
            </span>
            <span className="block text-2xs text-faint">
              {session.kind === "token" ? "token, " : ""}
              {session.scope}
            </span>
          </div>
          <button
            type="button"
            role="menuitem"
            className={menuItem}
            onClick={() => {
              close();
              navigate({ to: "/account" });
            }}
          >
            <Icon name="users" className="text-mut" /> Your account
          </button>
          <button
            type="button"
            role="menuitem"
            className={menuItem}
            onClick={() => {
              close();
              openHelp();
            }}
          >
            <Icon name="help" className="text-mut" /> Help
          </button>
          <hr className="my-1 border-0 border-t border-line" />
          <button
            type="button"
            role="menuitem"
            className={menuItem}
            disabled={logout.isPending}
            onClick={() => logout.mutate()}
          >
            <Icon name="logout" className="text-mut" /> Log out
          </button>
        </>
      )}
    </Menu>
  );
}

/**
 * What waits in the Inbox: open alerts (an access request has one), in
 * the worst one's colour; nothing when none.
 */
function InboxCount() {
  return <OpenAlertCount open={useOrgOpenAlerts()} bare />;
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
function OpenAlertCount({ open, bare }: { open: Alert[]; bare?: boolean }) {
  if (open.length === 0) return null;
  const critical = open.some((a) => a.severity === "critical");
  return (
    <span className={`text-2xs ${critical ? "text-fail" : "text-warn"}`}>
      {open.length}
      {/* The console's row says "alerts"; the Inbox's count is its own. */}
      {bare ? (
        <span className="sr-only"> waiting</span>
      ) : (
        ` ${open.length === 1 ? "alert" : "alerts"}`
      )}
    </span>
  );
}

/**
 * Projects, each with the services of one environment: the current
 * project the one in the URL, others production. Switching environments
 * is the project header's job.
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
  if (projects.length === 0) {
    return (
      <>
        <p className="py-1 pl-[24px] text-xs text-faint">No projects yet</p>
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
        const env =
          (current &&
            environments?.find(
              (e) => e.environment.name === here.environment,
            )) ||
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
 * the project has: names counted once across its environments, so the
 * number doesn't change with the environment the tree shows.
 */
function ProjectNote({ services }: { services: ServiceView[] }) {
  const failed = services.filter((s) => serviceState(s).tone === "fail");
  if (services.some((s) => s.deploying)) {
    return <span className="text-2xs text-warn">deploying</span>;
  }
  if (failed.length > 0) {
    return <span className="text-2xs text-fail">{failed.length} failed</span>;
  }
  const count = new Set(services.map((s) => s.service.name)).size;
  const words = plural(count, "service");
  return (
    <span title={words} className="text-2xs text-faint">
      <span aria-hidden>{count}</span>
      <span className="sr-only">{words}</span>
    </span>
  );
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
        className={`max-w-28 truncate text-2xs ${state.tone === "ok" || state.tone === "mut" ? "text-faint" : toneText[state.tone]}`}
      >
        {note}
      </span>
    </Link>
  );
}

/**
 * The platform console's sidebar (ADR 0099): a header row, "Platform" with
 * a quiet console label, that leads back to projects, then its nine entries by job
 * (features/console/nav.ts), each marked on any of its pages, with Updates
 * by the account at the foot. Only operators reach it.
 */
function ConsoleNav() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const here = consoleEntryOf(path);
  const entries = consoleNav.filter((e) => e.label !== "Updates");
  const updates = consoleNav.find((e) => e.label === "Updates");
  return (
    <>
      {/* In place of the organization: the console has none (ADR 0062). */}
      {/* One header row, all of it the way back (Saile's pick C): the title
          left, "Projects" right. On wide screens it is as tall as the page
          bar beside it, so their bottom lines meet, and 18px down like that
          bar's crumb slot (ui/kit.tsx), so the two titles share a line.
          Narrow screens put it under the Navigation button, plain.
          No preload: it would read the org's projects from inside the
          console, which sends no organization (api/client.ts). */}
      <Link
        to="/"
        preload={false}
        className="group -mx-2 mb-2 flex min-h-[44px] items-center gap-2 border-b border-line px-[18px] hover:bg-hover lg:-mt-2.5 lg:min-h-[64px] lg:pt-[18px]"
      >
        <span className="font-semibold">Platform</span>
        <span className="text-2xs text-warn">console</span>
        <span className="flex-1" />
        <span className="text-xs text-mut group-hover:text-fg">
          <span aria-hidden>← </span>Projects
        </span>
      </Link>
      {entries.map((e) => (
        <ConsoleLink key={e.label} entry={e} current={e === here}>
          {e.label === "Alerts" ? (
            <>
              <span className="flex-1">Alerts</span>
              <PlatformAlertCount />
            </>
          ) : (
            e.label
          )}
        </ConsoleLink>
      ))}
      <div className="min-h-2 flex-1" />
      {/* Updates by the account: both are about this Dock itself. */}
      {updates && (
        <ConsoleLink entry={updates} current={updates === here}>
          <span className="flex-1">Updates</span>
          <UpdateBadge />
        </ConsoleLink>
      )}
      <Me />
    </>
  );
}

/**
 * A console sidebar entry: opens its first page, current on any of its
 * pages (a server's own page counts under Servers).
 */
function ConsoleLink({
  entry,
  current,
  children,
}: {
  entry: ConsoleEntry;
  current: boolean;
  children: ReactNode;
}) {
  return (
    // Exact: the router's own mark would otherwise light Overview
    // ("/console") on every console page; `current` marks the entry on
    // any of its pages.
    <Link
      to={entry.pages[0].to}
      className="nav-row"
      activeOptions={{ exact: true }}
      aria-current={current ? "page" : undefined}
    >
      <Icon name={entry.icon} className="text-mut" />
      {children}
    </Link>
  );
}
