import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/console/")({
  beforeLoad: () => {
    throw redirect({ to: "/console/servers" });
  },
});
