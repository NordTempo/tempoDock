import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// A project's Variables settings open on the environment whose changes
// wait, else the first.
export const Route = createFileRoute("/_app/$org/$project/settings/variables/")(
  {
    beforeLoad: async ({ context: { queryClient }, params }) => {
      const { environments } = await queryClient.ensureQueryData(
        queries.overview(params.project),
      );
      const env =
        environments.find((e) => e.pending_changes > 0) ?? environments[0];
      throw env
        ? redirect({
            to: "/$org/$project/settings/variables/$environment",
            params: { ...params, environment: env.environment.name },
            replace: true,
          })
        : redirect({
            to: "/$org/$project/settings",
            params,
            replace: true,
          });
    },
  },
);
