import { createFileRoute, redirect } from "@tanstack/react-router";
import { siteRedirect } from "../features/analytics/loaders";

// The old Analytics sub-tabs (…/web/analytics/vitals, /replays/<id>):
// their site's page in the Analytics area, errors the service's Errors
// tab (ADR 0059).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/$",
)({
  beforeLoad: ({ context: { queryClient }, params, search }) => {
    const { _splat: rest = "", ...ref } = params;
    if (rest === "errors")
      throw redirect({
        to: "/$org/$project/$environment/$service/errors",
        params: ref,
        search,
      });
    return siteRedirect(queryClient, ref, search, rest);
  },
});
