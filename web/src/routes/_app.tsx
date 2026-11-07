import { createFileRoute, redirect } from "@tanstack/react-router";
import { setInConsole } from "../api/client";
import { queries } from "../data/queries";
import { Shell } from "../shell/Shell";
import { ShellPending } from "../ui/RouteStates";

// Pathless layout for everything behind login: sidebar, palette, live updates.
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location, preload }) => {
    // The console's calls go without the current organization; leaving
    // it, they carry it again (api/client.ts). Only a real navigation
    // decides: a hovered link's preload must not flip it.
    if (!preload)
      setInConsole(
        location.pathname === "/console" ||
          location.pathname.startsWith("/console/"),
      );
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
    // The shell shows one organization's projects; with none, there's a
    // page saying so (and invite links work outside the shell). An org
    // that requires a second factor (ADR 0057) or single sign-on (ADR
    // 0058) the session didn't use leaves only the account page, with
    // the way back.
    if (!session.org && session.kind === "user") {
      if (!session.two_factor_required && !session.sso_required) {
        throw redirect({ to: "/no-org" });
      }
      if (location.pathname !== "/account") throw redirect({ to: "/account" });
    }
  },
  // The sidebar lists the projects and the org switcher the caller's
  // organizations. Every project's services (the sidebar tree, the
  // palette) load in the background: a slow or failing project fills in
  // or fails on its own page, without holding up the shell.
  loader: async ({ context: { queryClient }, location }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.kind === "user" && !session.org) return;
    // The console has no organization: its pages load their own data.
    if (location.pathname.startsWith("/console")) return;
    const [projects] = await Promise.all([
      queryClient.ensureQueryData(queries.projects()),
      queryClient.ensureQueryData(queries.servers()),
      queryClient.ensureQueryData(queries.orgs()),
    ]);
    for (const p of projects) {
      void queryClient.prefetchQuery(queries.overview(p.name));
    }
  },
  component: Shell,
  // A slow first load shows the frame, not a blank page.
  pendingComponent: ShellPending,
  pendingMs: 300,
});
