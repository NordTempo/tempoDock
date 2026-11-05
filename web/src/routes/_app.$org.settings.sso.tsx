import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { SSOPage } from "../features/org/SSOPage";

// The organization's single sign-on providers and requiring them, for its
// admins (ADR 0058).
export const Route = createFileRoute("/_app/$org/settings/sso")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.scope === "admin") {
      await Promise.all([
        queryClient.ensureQueryData(orgQueries.ssoProviders()),
        queryClient.ensureQueryData(orgQueries.org()),
      ]);
    }
  },
  component: SSOPage,
});
