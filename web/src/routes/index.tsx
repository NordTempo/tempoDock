import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { $api } from "../api/client";

const health = $api.queryOptions("get", "/api/v1/health");

// Placeholder shell until the UI design is picked.
export const Route = createFileRoute("/")({
  loader: ({ context }) => context.queryClient.ensureQueryData(health),
  component: Home,
});

function Home() {
  const { data } = useSuspenseQuery(health);
  return <p className="p-4 font-mono">daemon {data.version}</p>;
}
