import { createFileRoute, redirect } from "@tanstack/react-router";
import { analyticsSearch } from "../features/analytics/loaders";

// The service's old Analytics tab: Requests' Visitors view now, with the
// site, range and filters it was opened with.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics",
)({
  validateSearch: analyticsSearch,
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/$org/$project/$environment/$service/requests/visitors",
      params,
      search,
      replace: true,
    });
  },
});
