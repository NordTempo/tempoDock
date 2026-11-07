import { createFileRoute } from "@tanstack/react-router";
import {
  PlatformPolicyPage,
  platformPolicyQuery,
} from "../features/console/PlatformPolicyPage";

// The platform's data access default and ceiling (ADR 0062). Each
// organization's own rules are in its settings.
export const Route = createFileRoute("/_app/console/data-access")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(platformPolicyQuery()),
  component: PlatformPolicyPage,
});
