import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// Organization settings open on Members for admins, Variables otherwise.
export const Route = createFileRoute("/_app/$org/settings/")({
  beforeLoad: async ({ context, params }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    throw redirect({
      to:
        session?.scope === "admin"
          ? "/$org/settings/members"
          : "/$org/settings/variables",
      params,
      replace: true,
    });
  },
});
