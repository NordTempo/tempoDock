import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// "/" goes to the organization the caller acts in (the last one used);
// _app sends people without one to /no-org.
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session?.org) {
      throw redirect({ to: "/$org", params: { org: session.org } });
    }
  },
});
