import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// The platform console: everything that runs Dock itself, for operators
// only (ADR 0027). Everyone else is sent back to their projects (the API
// refuses them too).
// The shell gives these pages the console sidebar and the amber bar.
export const Route = createFileRoute("/_app/console")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session?.operator) throw redirect({ to: "/" });
  },
});
