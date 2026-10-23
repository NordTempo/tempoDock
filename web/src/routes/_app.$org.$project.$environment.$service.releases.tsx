import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { queries, servicePath, useReleases, useService } from "../data/queries";
import { ReleasesTable } from "../features/service/ReleasesTable";
import { Act, ErrorText } from "../ui/kit";

/** The most releases the API returns at once (`dock release list -n`). */
const MAX = 100;

// The release history: the newest 20, up to 100 on request.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/releases",
)({
  component: ReleasesTab,
});

function ReleasesTab() {
  const ref = servicePath(Route.useParams());
  const { deploying } = useService(ref);
  const newest = useReleases(ref);
  const [more, setMore] = useState(false);
  const all = useQuery({ ...queries.releases(ref, MAX), enabled: more });
  const releases = all.data ?? newest;
  return (
    <>
      <ReleasesTable
        serviceRef={ref}
        releases={releases}
        deploying={!!deploying}
      />
      {!all.data && newest.length === 20 && (
        <div className="-mt-3 flex items-center gap-3">
          <Act
            small
            cli={`dock release list -n ${MAX} -s ${ref.service} -e ${ref.environment}`}
            disabled={all.isFetching}
            onClick={() => setMore(true)}
          >
            Show up to {MAX}
          </Act>
          <ErrorText error={all.error} />
        </div>
      )}
    </>
  );
}
