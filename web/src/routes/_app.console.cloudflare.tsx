import { createFileRoute } from "@tanstack/react-router";
import { CloudflarePage } from "../features/cloudflare/CloudflarePage";
import { cloudflareQuery } from "../features/cloudflare/data";

// Cloudflare connections: the API tokens Dock runs tunnels and routes
// domains with (ADR 0035).
export const Route = createFileRoute("/_app/console/cloudflare")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(cloudflareQuery()),
  component: CloudflarePage,
});
