import { createFileRoute } from "@tanstack/react-router";
import { CloudflarePage } from "../features/cloudflare/CloudflarePage";
import { cloudflareQuery, redirectsQuery } from "../features/cloudflare/data";

// The org's own Cloudflare connections (ADR 0062): domains from its own
// zones, and its redirects (ADR 0068, ?tab=redirects). The platform's
// are the console's.
export const Route = createFileRoute("/_app/$org/settings/cloudflare")({
  validateSearch: (s: Record<string, unknown>): { tab?: "redirects" } =>
    s.tab === "redirects" ? { tab: "redirects" } : {},
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(cloudflareQuery("org")),
      queryClient.ensureQueryData(redirectsQuery("org")),
    ]),
  component: function Cloudflare() {
    const { tab = "connections" } = Route.useSearch();
    return <CloudflarePage scope="org" tab={tab} />;
  },
});
