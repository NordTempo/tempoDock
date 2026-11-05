import { createFileRoute } from "@tanstack/react-router";
import { siteRedirect } from "../features/analytics/loaders";

// The service's old Analytics tab: its site's page in the Analytics area
// now (ADR 0059).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics",
)({
  beforeLoad: ({ context: { queryClient }, params, search }) =>
    siteRedirect(queryClient, params, search, ""),
});
