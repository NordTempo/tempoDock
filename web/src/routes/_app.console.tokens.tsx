import { createFileRoute } from "@tanstack/react-router";
import {
  PlatformTokensPage,
  platformTokensQuery,
} from "../features/console/PlatformTokens";

// The platform's tokens (ADR 0080): platform tokens, platform logins and
// legacy tokens to split.
export const Route = createFileRoute("/_app/console/tokens")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(platformTokensQuery()),
  component: PlatformTokensPage,
});
