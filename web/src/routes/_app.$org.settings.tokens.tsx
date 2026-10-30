import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { TokensPage } from "../features/tokens/TokensPage";

// The organization's tokens, for its admins. Everyone sees their own on
// their account page.
export const Route = createFileRoute("/_app/$org/settings/tokens")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.tokens()),
  component: TokensPage,
});
