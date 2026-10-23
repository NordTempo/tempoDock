import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { TokensPage } from "../features/tokens/TokensPage";

// Every token on this Dock: who made it, its scope and limit, when it was
// last used.
export const Route = createFileRoute("/_app/console/tokens")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.tokens()),
  component: TokensPage,
});
