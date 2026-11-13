import { createFileRoute, notFound, redirect } from "@tanstack/react-router";
import { api, currentOrg, setOrg } from "../api/client";
import { queries } from "../data/queries";
import { RoutePending } from "../shell/Shell";
import { NotFound } from "../ui/RouteStates";

// Everything in one organization (/$org/...): the API calls act in the
// org in the URL. Moving to another org drops what was cached for the
// last one, so no page shows another org's data. A hovered link's
// preload runs this too: one into another org changes nothing and loads
// nothing, since the cache holds one org at a time. Switching there would
// drop the queries the page being opened is still loading, which then
// fails with a CancelledError. A slug the org had before it changed it
// (ADR 0064) redirects to the same page at its new one, so old links and
// bookmarks keep working.
export const Route = createFileRoute("/_app/$org")({
  beforeLoad: async ({
    context: { queryClient },
    params,
    location,
    preload,
    abortController,
  }) => {
    if (preload && params.org !== currentOrg()) {
      abortController.abort();
      return;
    }
    const orgs = await queryClient.ensureQueryData(queries.orgs());
    if (!orgs.some((o) => o.slug === params.org)) {
      const moved = await movedOrg(params.org);
      if (!moved || !orgs.some((o) => o.slug === moved)) throw notFound();
      const rest = location.pathname.slice(params.org.length + 1);
      throw redirect({
        href: `/${moved}${rest}${location.searchStr}${location.hash ? `#${location.hash}` : ""}`,
        replace: true,
      });
    }
    if (params.org === currentOrg()) return;
    setOrg(params.org);
    queryClient.removeQueries({
      predicate: (q) => q.queryKey[0] !== "orgs",
    });
    await queryClient.ensureQueryData(queries.session());
  },
  // Pages under an org that don't exist render inside the shell.
  notFoundComponent: NotFound,
  // A slow page loads inside the shell, which stays usable meanwhile.
  pendingComponent: RoutePending,
});

/**
 * The slug of the org that slug names now, when it is one the org had;
 * undefined when it names none of the caller's. Asked only for a slug
 * that isn't among the caller's orgs.
 */
async function movedOrg(slug: string) {
  const { data } = await api.GET("/api/v1/org", {
    headers: { "Dock-Org": slug },
  });
  return data?.slug;
}
