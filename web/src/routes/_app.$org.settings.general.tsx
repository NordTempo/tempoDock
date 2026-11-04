import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { GeneralPage } from "../features/org/GeneralPage";

export const Route = createFileRoute("/_app/$org/settings/general")({
  loader: async ({ context: { queryClient } }) => {
    const [, session] = await Promise.all([
      queryClient.ensureQueryData(orgQueries.org()),
      queryClient.ensureQueryData(queries.session()),
    ]);
    // Lock settings are shown to admins only.
    if (session?.scope === "admin") {
      await queryClient.ensureQueryData(orgQueries.lockSettings());
    }
  },
  component: GeneralPage,
});
