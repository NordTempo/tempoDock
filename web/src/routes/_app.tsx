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
  },
  // The sidebar tree and palette show every project's services.
  loader: async ({ context: { queryClient } }) => {
    const [projects] = await Promise.all([
      queryClient.ensureQueryData(queries.projects()),
      queryClient.ensureQueryData(queries.servers()),
    ]);
    await Promise.all(
      projects.map((p) =>
        queryClient.ensureQueryData(queries.overview(p.name)),
      ),
    );
  },
  component: Shell,
});
