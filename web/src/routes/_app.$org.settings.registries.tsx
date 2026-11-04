import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { RegistriesPage } from "../features/org/RegistriesPage";

// The organization's private registry credentials, for its admins.
export const Route = createFileRoute("/_app/$org/settings/registries")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.scope === "admin") {
      await queryClient.ensureQueryData(orgQueries.registryCredentials());
    }
  },
  component: RegistriesPage,
});
