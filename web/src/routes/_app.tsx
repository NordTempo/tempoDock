import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { Shell } from "../shell/Shell";

// Pathless layout for everything behind login: sidebar, palette, live updates.
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
    // The shell shows one organization's projects; with none, there's a
    // page saying so (and invite links work outside the shell). An org
    // that requires a second factor the session didn't use leaves only
    // the account page, to add one (ADR 0050).
    if (!session.org && session.kind === "user") {
      if (!session.two_factor_required) throw redirect({ to: "/no-org" });
      if (location.pathname !== "/account") throw redirect({ to: "/account" });
    }
  },
  // The sidebar tree and palette show every project's services, and the
  // org switcher the caller's organizations.
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.kind === "user" && !session.org) return;
    const [projects] = await Promise.all([
      queryClient.ensureQueryData(queries.projects()),
      queryClient.ensureQueryData(queries.servers()),
      queryClient.ensureQueryData(queries.orgs()),
    ]);
    await Promise.all(
      projects.map((p) =>
        queryClient.ensureQueryData(queries.overview(p.name)),
      ),
    );
  },
  component: Shell,
});
