import { createFileRoute, notFound } from "@tanstack/react-router";
import { currentOrg, setOrg } from "../api/client";
import { queries } from "../data/queries";

// Everything in one organization (/$org/...): the API calls act in the
// org in the URL. Moving to another org drops what was cached for the
// last one, so no page shows another org's data.
export const Route = createFileRoute("/_app/$org")({
  beforeLoad: async ({ context: { queryClient }, params }) => {
    if (params.org === currentOrg()) return;
    const orgs = await queryClient.ensureQueryData(queries.orgs());
    if (!orgs.some((o) => o.slug === params.org)) throw notFound();
    setOrg(params.org);
    queryClient.removeQueries({
      predicate: (q) => q.queryKey[0] !== "orgs",
    });
    await queryClient.ensureQueryData(queries.session());
  },
});
