import { Link, Outlet } from "@tanstack/react-router";
import { useLiveUpdates } from "../data/live";
import { useLogout } from "../data/mutations";
import { useSession } from "../data/queries";
import { Icon } from "../ui/icons";
import { CommandLine } from "./CommandLine";
import { PaletteProvider } from "./palette";
import { Sidebar, useShellMode } from "./Sidebar";

/**
 * Everything behind login: the sidebar, the page (which brings its own
 * PageBar), and the command line at the bottom. The platform console
 * (servers) gets an amber bar across the top. A person whose org needs a
 * second factor they didn't sign in with acts in none: they get their
 * account page alone, to add one (ADR 0057).
 */
export function Shell() {
  const session = useSession();
  if (session?.kind === "user" && !session.org) return <AccountOnly />;
  return <OrgShell />;
}

function OrgShell() {
  useLiveUpdates();
  const mode = useShellMode();
  return (
    <PaletteProvider>
      <a
        href="#main"
        className="sr-only z-50 rounded-ctl bg-fg px-3 py-1.5 text-side focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      {mode === "console" && <ConsoleBar />}
      <div className="flex min-h-screen flex-col lg:flex-row">
        <Sidebar mode={mode} />
        <main id="main" className="flex min-w-0 flex-1 flex-col">
          <TwoFactorNotice />
          <div className="flex flex-1 flex-col">
            <Outlet />
          </div>
          <CommandLine />
        </main>
      </div>
    </PaletteProvider>
  );
}

/** The console's reminder that changes here reach every project. */
function ConsoleBar() {
  return (
    <div className="sticky top-0 z-30 flex min-h-8 flex-wrap items-center gap-x-2.5 gap-y-1 border-b border-warn-edge bg-warn-bg px-4 py-1.5 text-xs">
      <Icon name="console" size={13} className="text-warn" />
      <span className="font-semibold text-warn">Platform console</span>
      <span className="text-warn-text">
        Changes here affect every project on this Dock.
      </span>
      <Link to="/" className="ml-auto text-warn-text underline">
        Back to projects
      </Link>
    </div>
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
 * a second factor: the notice and their account page, nothing that
 * reads an org (projects, servers, live events).
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
