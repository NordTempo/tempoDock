import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { Shell } from "../shell/Shell";

// Pathless layout for everything behind sign-in.
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (!me) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: Shell,
});
