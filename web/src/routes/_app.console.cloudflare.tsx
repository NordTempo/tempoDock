import { createFileRoute } from "@tanstack/react-router";
import { CloudflarePage } from "../features/cloudflare/CloudflarePage";
import { cloudflareQuery, redirectsQuery } from "../features/cloudflare/data";
import { ConsoleTabs } from "../features/console/ConsoleTabs";

// The platform's Cloudflare connections (ADR 0036, 0062): Dock's address,
// server tunnels, and zones shared with organizations; ?tab=redirects
// holds the platform's redirects (ADR 0068).
export const Route = createFileRoute("/_app/console/cloudflare")({
  validateSearch: (s: Record<string, unknown>): { tab?: "redirects" } => ({
    tab: s.tab === "redirects" ? "redirects" : undefined,
  }),
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(cloudflareQuery("platform")),
      queryClient.ensureQueryData(redirectsQuery("platform")),
    ]),
  component: function Cloudflare() {
    const { tab = "connections" } = Route.useSearch();
    return (
      <ConsoleTabs>
        <CloudflarePage scope="platform" tab={tab} />
      </ConsoleTabs>
    );
  },
});
