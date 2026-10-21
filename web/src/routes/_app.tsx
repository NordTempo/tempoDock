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
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.org()),
      queryClient.ensureQueryData(queries.projects()),
      queryClient.ensureQueryData(queries.servers()),
    ]),
  component: Shell,
});
