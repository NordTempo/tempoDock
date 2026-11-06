import { createFileRoute, redirect } from "@tanstack/react-router";

// Organization settings open on General, which everyone may see.
export const Route = createFileRoute("/_app/$org/settings/")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/$org/settings/general", params, replace: true });
  },
});
