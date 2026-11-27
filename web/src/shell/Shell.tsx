import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useLiveUpdates } from "../data/live";
import { useLogout } from "../data/mutations";
import { useSession } from "../data/queries";
import { HelpHost } from "../features/help/open";
import { QueueHost } from "../features/queue/open";
import { DrainStrip } from "../features/servers/DrainStrip";
import { Icon } from "../ui/icons";
import { Notices } from "../ui/notice";
import { PagePending } from "../ui/RouteStates";
import { modKey } from "./keys";
import { NotLive } from "./NotLive";
import { PaletteProvider } from "./palette";
import {
  columnClass,
  OrgBadge,
  orgViews,
  Sidebar,
  TopBar,
  topBarClass,
  topLinkClass,
  useProjectsColumn,
  useShellMode,
  useTreeOpen,
} from "./Sidebar";

/**
 * Everything behind login: the sidebar and the page (which brings its own
 * PageBar). One command surface, the palette (Cmd+K or the top bar's
 * search), finds pages, settings and commands; ? opens help for the page,
 * with its keys. The platform console has no top bar: its sidebar says
 * where you are and has the way back. A person whose org needs a
 * second factor (ADR 0057) or single sign-on (ADR 0058) they didn't sign
 * in with acts in none: they get their account page alone, with the way
 * back.
 */
export function Shell() {
  const session = useSession();
  if (session?.kind === "user" && !session.org) return <AccountOnly />;
  return <OrgShell />;
}

function OrgShell() {
  useLiveUpdates();
  const mode = useShellMode();
  const demo = useSession()?.demo === true;
  // The project tree sits beside Projects and the pages inside a project.
  const projects = useProjectsColumn();
  return (
    <PaletteProvider>
      <a
        href="#main"
        className="fixed top-2 left-2 z-50 -translate-y-16 rounded-ctl bg-fg px-3 py-1.5 text-side focus:translate-y-0"
      >
        Skip to content
      </a>
      {/* One sticky stack, so the not-live strip sits under the bars
          instead of sticking over them (index.css counts it in --shell-top). */}
      <div className="sticky top-0 z-30">
        {demo && <DemoBar />}
        {mode === "app" && <TopBar />}
        <NotLive />
      </div>
      {mode === "console" && <DrainStrip />}
      <div className="flex min-h-screen flex-col lg:flex-row">
        {(mode === "console" || projects) && <Sidebar mode={mode} />}
        <main id="main" className="flex min-w-0 flex-1 flex-col">
          <TwoFactorNotice />
          <div className="flex flex-1 flex-col">
            <Outlet />
          </div>
        </main>
      </div>
      <HelpHost />
      <QueueHost console={mode === "console"} />
      <Notices />
    </PaletteProvider>
  );
}

/**
 * A page still loading, inside the shell: the navigation stays and the
 * page shows its still frame (PagePending), shaped like the page by its
 * address. The org and console routes use it, so a slow page never
 * blanks the navigation.
 */
export const RoutePending = PagePending;

/**
 * The shell as it will look, while its first data loads (a cold start,
 * _app's pendingComponent): the top bar's fixed parts (none on the
 * console), the left column at the width it will have (the saved tree or
 * rail choice) and the page's
 * frame. Nothing here reads a query; the org is the address's slug. The
 * column shows where the shell shows it (useProjectsColumn).
 */
export function ShellPending() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const inConsole = path === "/console" || path.startsWith("/console/");
  const [saved] = useTreeOpen();
  const projects = useProjectsColumn();
  const wide = inConsole || saved;
  return (
    <>
      <div className="sticky top-0 z-30">
        {!inConsole && <TopBarFrame path={path} />}
      </div>
      <div className="flex min-h-screen flex-col lg:flex-row">
        {(inConsole || projects) && (
          <div className={columnClass(wide)}>
            <span className="nav-row text-mut lg:hidden">
              <Icon name="list" />
              Navigation
            </span>
            <div className="hidden flex-col gap-0.5 lg:flex">
              {wide ? (
                <>
                  {/* ConsoleNav's heading, or AppNav's row over the tree. */}
                  {inConsole ? (
                    <span className="px-2.5 pt-1 pb-2 font-semibold">
                      Platform
                    </span>
                  ) : (
                    <span className="nav-row text-2xs font-medium text-faint">
                      Projects
                    </span>
                  )}
                  {["w-24", "w-20", "w-28"].map((w) => (
                    <span key={w} className="nav-row">
                      <span
                        className={`block h-[10px] rounded-tag bg-dash ${w}`}
                      />
                    </span>
                  ))}
                </>
              ) : (
                <>
                  <span className="nav-row justify-center px-0 text-mut">
                    <Icon name="chevronRight" size={14} />
                  </span>
                  <span className="flex flex-col items-center gap-1">
                    {[0, 1, 2].map((i) => (
                      <span
                        key={i}
                        className="block size-9 rounded-ctl border border-edge"
                      />
                    ))}
                  </span>
                </>
              )}
            </div>
          </div>
        )}
        <main id="main" className="flex min-w-0 flex-1 flex-col">
          <PagePending />
        </main>
      </div>
    </>
  );
}

/**
 * TopBar's fixed parts in their places, for ShellPending: the org's
 * badge and slug (its name isn't loaded yet), its views as links, Search,
 * Inbox, Settings, ? and the account's circle.
 */
function TopBarFrame({ path }: { path: string }) {
  const [org = "", area] = path.split("/").filter(Boolean);
  // The view the address is in, from the path alone: the router's own
  // active state isn't settled while the first load is pending.
  const here = area ? `/$org/${area}` : "/$org";
  return (
    <header className={topBarClass}>
      <span className="flex items-center gap-2 p-1.5">
        <OrgBadge org={{ slug: org, name: org }} />
        <span className="hidden font-semibold sm:inline">{org}</span>
        <Icon name="chevronDown" size={12} className="text-mut" />
      </span>
      <nav
        aria-label="Organization views"
        className="hidden items-center gap-0.5 md:flex"
      >
        {orgViews.map(([to, label]) => (
          <Link
            key={to}
            to={to}
            params={{ org }}
            // Current by `here`; exact keeps the router from marking a parent too.
            activeOptions={{ exact: true }}
            aria-current={to === here ? "page" : undefined}
            className={topLinkClass}
          >
            {label}
          </Link>
        ))}
      </nav>
      <span className="flex-1" />
      <span className="flex min-h-[28px] items-center gap-2 rounded-ctl border border-line px-2.5 text-mut">
        <Icon name="search" />
        <span className="sr-only sm:not-sr-only">Search</span>
        <kbd className="hidden sm:inline">{modKey.trim()}K</kbd>
      </span>
      <Link to="/$org/inbox" params={{ org }} className={topLinkClass}>
        Inbox
      </Link>
      <Link to="/$org/settings" params={{ org }} className={topLinkClass}>
        Settings
      </Link>
      <span
        aria-hidden
        className="hidden min-h-[28px] items-center px-2.5 text-mut sm:flex"
      >
        ?
      </span>
      <span aria-hidden className="size-7 rounded-full bg-edge" />
    </header>
  );
}

/**
/**
 * The public demo's strip (ADR 0100), above every page: that it's
 * view-only, and where to get Dock. A fixed 30px, which index.css
 * counts in --shell-top through the `demo-bar` class.
 */
function DemoBar() {
  return (
    <p className="demo-bar flex h-[30px] items-center gap-4 overflow-hidden border-b border-line bg-page px-4 text-xs whitespace-nowrap text-sec">
      <span className="font-semibold text-fg">Demo</span>
      <span className="min-w-0 truncate">
        View-only, with a sample company's data.
      </span>
      <a
        href="https://dock.nordtempo.com/docs/install"
        className="ml-auto inline-flex min-h-[24px] items-center text-fg underline underline-offset-[3px]"
      >
        <span className="hidden sm:inline">Install Dock on your server</span>
        <span className="sm:hidden">Install Dock</span>
      </a>
    </p>
  );
}

/**
 * What a second factor would unlock (Me.two_factor_required): org pages
 * and the console answer two_factor_required until the person signs in
 * with one, so say so on every page.
 */
function TwoFactorNotice() {
  const needs = useSession()?.two_factor_required;
  if (!needs) return null;
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-warn-edge bg-stage-bg px-5 py-2 text-xs"
    >
      <span className="text-warn">Two-factor sign-in required</span>
      <span className="min-w-0 flex-1 text-warn-text">{needs}.</span>
      <Link to="/account" className="text-warn-text underline">
        Your account
      </Link>
    </div>
  );
}

/**
 * The shell with no organization to act in, because the person's needs
 * a second factor or single sign-on: the notice and their account page,
 * nothing that reads an org (projects, servers, live events).
 */
function AccountOnly() {
  const logout = useLogout();
  return (
    <main id="main" className="flex min-h-screen flex-col">
      <TwoFactorNotice />
      <div className="flex flex-1 flex-col">
        <Outlet />
      </div>
      <div className="px-4 pb-6 sm:px-7">
        <button
          type="button"
          className="btn"
          disabled={logout.isPending}
          onClick={() => logout.mutate()}
        >
          Log out
        </button>
      </div>
    </main>
  );
}
