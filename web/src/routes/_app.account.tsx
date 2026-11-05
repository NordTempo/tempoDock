import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AccountPage } from "../features/account/AccountPage";
import { twoFactorQuery } from "../features/account/data";
import { ownTokensQuery } from "../features/tokens/data";

// The signed-in person's own account: who they are, their two-factor
// sign-in, their tokens and their password.
export const Route = createFileRoute("/_app/account")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.kind === "user") {
      await Promise.all([
        queryClient.ensureQueryData(ownTokensQuery()),
        queryClient.ensureQueryData(twoFactorQuery()),
      ]);
    }
  },
  component: AccountPage,
});
