import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// "/" lands on the first project's overview.
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const [first] = await context.queryClient.ensureQueryData(
      queries.projects(),
    );
    if (!first) throw redirect({ to: "/servers" });
    throw redirect({
      to: "/projects/$projectId",
      params: { projectId: first.id },
    });
  },
});
