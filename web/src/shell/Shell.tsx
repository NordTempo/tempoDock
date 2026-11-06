import { Link, Outlet } from "@tanstack/react-router";
import { useState } from "react";
import { currentOrg, setOrg } from "../api/client";
import { useLiveUpdates } from "../data/live";
import { useLogout } from "../data/mutations";
import { useSession } from "../data/queries";
import { type SSOChoice, useStartSSO } from "../features/sso/data";
import { Icon } from "../ui/icons";
import { ErrorText } from "../ui/kit";
import { Notices } from "../ui/notice";
import { KeyHelpHost, NotLive } from "./KeyHelp";
import { PaletteProvider } from "./palette";
import { Sidebar, useShellMode } from "./Sidebar";

/**
 * Everything behind login: the sidebar and the page (which brings its own
 * PageBar). One command surface, the palette (Cmd+K or the sidebar's
 * search), finds pages and runs commands; ? lists the keys. The platform
 * console gets an amber bar across the top. A person whose org needs a
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
  return (
    <PaletteProvider>
      <a
        href="#main"
        className="fixed top-2 left-2 z-50 -translate-y-16 rounded-ctl bg-fg px-3 py-1.5 text-side focus:translate-y-0"
      >
        Skip to content
      </a>
      {mode === "console" && <ConsoleBar />}
      <div className="flex min-h-screen flex-col lg:flex-row">
        <Sidebar mode={mode} />
        <main id="main" className="flex min-w-0 flex-1 flex-col">
          <NotLive />
          <TwoFactorNotice />
          <div className="flex flex-1 flex-col">
            <Outlet />
          </div>
        </main>
      </div>
      <KeyHelpHost />
      <Notices />
    </PaletteProvider>
  );
}

/**
 * A page still loading, inside the shell: the sidebar stays, the page
 * says Loading over a header bar and a table's frame with still rows (no
 * shimmer). The org and console routes use it, so a slow page
 * never blanks the navigation.
 */
export function RoutePending() {
  return (
    <div aria-busy="true" className="flex flex-1 flex-col">
      <div className="flex h-[52px] items-center border-b border-line px-5">
        <span className="h-[12px] w-40 rounded-[3px] bg-sel" />
      </div>
      <div className="flex w-full max-w-[1400px] flex-col gap-3 px-4 pt-6 sm:px-7">
        <p role="status" className="text-mut">
          Loading…
        </p>
        <span className="h-[12px] w-32 rounded-[3px] bg-sel" />
        {/* A table's frame with a few still rows, like most pages. */}
        <div className="flex flex-col rounded-panel border border-edge">
          {[0, 1, 2, 3].map((i) => (
            <div
              key={i}
              className="flex gap-6 border-b border-line px-3.5 py-3 last:border-b-0"
            >
              <span className="h-[10px] w-28 rounded-[3px] bg-sel" />
              <span className="h-[10px] w-20 rounded-[3px] bg-sel" />
              <span className="h-[10px] w-48 rounded-[3px] bg-sel" />
            </div>
          ))}
        </div>
      </div>
    </div>
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
      <Link
        to="/"
        className="ml-auto inline-flex min-h-[24px] items-center text-warn-text underline"
      >
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
 * Why a single sign-on rule leaves the person in no organization
 * (Me.sso_required, ADR 0058), and the way in: signing in through the
 * organization's SSO, or for a single sign-on session that wandered into
 * another organization, back to its own.
 */
function SSONotice() {
  const me = useSession();
  const start = useStartSSO();
  // The organization's providers, when it has several.
  const [choices, setChoices] = useState<SSOChoice[]>([]);
  if (!me?.sso_required) return null;
  const org = currentOrg();
  const link = "text-warn-text underline disabled:opacity-60";
  const own = me.sso;
  let action = null;
  if (own && own !== org) {
    action = (
      <button
        type="button"
        className={link}
        onClick={() => {
          setOrg(own);
          window.location.assign("/");
        }}
      >
        Back to {own}
      </button>
    );
  } else if (!own && me.email) {
    const email = me.email;
    action =
      choices.length > 0 ? (
        choices.map((c) => (
          <button
            key={c.provider}
            type="button"
            className={link}
            disabled={start.isPending}
            onClick={() =>
              start.mutate({ org: c.org, provider: c.provider, to: "/" })
            }
          >
            Sign in with {c.provider}
          </button>
        ))
      ) : (
        <button
          type="button"
          className={link}
          disabled={start.isPending}
          onClick={() =>
            start.mutate(
              { email, to: "/", only: org },
              { onSuccess: (c) => setChoices(c ?? []) },
            )
          }
        >
          Sign in with SSO
        </button>
      );
  }
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-warn-edge bg-stage-bg px-5 py-2 text-xs"
    >
      <span className="text-warn">Single sign-on required</span>
      <span className="min-w-0 flex-1 text-warn-text">{me.sso_required}.</span>
      {action}
      <ErrorText error={start.error} />
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
      <SSONotice />
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
