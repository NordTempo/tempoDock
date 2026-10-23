import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { TokensPage } from "../features/tokens/TokensPage";

// The organization's tokens, for its admins. Operators manage them in the
// platform console.
export const Route = createFileRoute("/_app/tokens")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session?.operator) throw redirect({ to: "/console/tokens" });
  },
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.tokens()),
  component: TokensPage,
});
