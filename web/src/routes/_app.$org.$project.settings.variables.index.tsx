import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";

// A project's variables open on the environment whose changes wait, else
// the page's own pick: the Variables section of its Settings page.
export const Route = createFileRoute("/_app/$org/$project/settings/variables/")(
  {
    beforeLoad: async ({ context: { queryClient }, params }) => {
      const { environments } = await queryClient.ensureQueryData(
        queries.overview(params.project),
      );
      const env = environments.find((e) => e.pending_changes > 0);
      throw redirect({
        to: "/$org/$project/settings",
        params,
        search: env ? { env: env.environment.name } : {},
        hash: "variables",
        replace: true,
      });
    },
  },
);
