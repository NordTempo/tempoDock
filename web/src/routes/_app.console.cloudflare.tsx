import { createFileRoute } from "@tanstack/react-router";
import { CloudflarePage } from "../features/cloudflare/CloudflarePage";
import { cloudflareQuery } from "../features/cloudflare/data";

// The platform's Cloudflare connections (ADR 0036, 0062): Dock's address,
// server tunnels, and zones shared with organizations.
export const Route = createFileRoute("/_app/console/cloudflare")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(cloudflareQuery("platform")),
  component: () => <CloudflarePage scope="platform" />,
});
