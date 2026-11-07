import { createFileRoute } from "@tanstack/react-router";
import { CloudflarePage } from "../features/cloudflare/CloudflarePage";
import { cloudflareQuery } from "../features/cloudflare/data";

// The org's own Cloudflare connections (ADR 0062): domains from its own
// zones. The platform's are the console's.
export const Route = createFileRoute("/_app/$org/settings/cloudflare")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(cloudflareQuery("org")),
  component: () => <CloudflarePage scope="org" />,
});
