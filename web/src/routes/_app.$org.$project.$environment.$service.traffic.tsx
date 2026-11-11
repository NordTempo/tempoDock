import { createFileRoute, redirect } from "@tanstack/react-router";

// The old Traffic tab: Requests' Traffic view now, range and all.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/traffic",
)({
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/requests",
      params,
      search,
      replace: true,
    });
  },
});
