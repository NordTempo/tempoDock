import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { MembersPage } from "../features/org/MembersPage";

export const Route = createFileRoute("/_app/$org/settings/members")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.scope === "admin") {
      await Promise.all([
        queryClient.ensureQueryData(orgQueries.members()),
        queryClient.ensureQueryData(orgQueries.invites()),
        queryClient.ensureQueryData(orgQueries.org()),
      ]);
    }
  },
  component: MembersPage,
});
