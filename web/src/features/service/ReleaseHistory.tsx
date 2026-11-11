import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { queries, useReleases } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Act, ErrorText, SectionHead } from "../../ui/kit";
import { ReleasesTable } from "./ReleasesTable";

/** The most releases the API returns at once (`dock release list -n`). */
const MAX = 100;

/**
 * The release history with rollback and diff: the newest 10, the 20 the
 * service's release query holds on request, then up to 100 (History).
 * `short`, on the Overview: the newest 5 and the way to History.
 */
export function ReleaseHistory({
  serviceRef: ref,
  deploying,
  short,
}: {
  serviceRef: ServiceRef;
  deploying: boolean;
  short?: boolean;
}) {
  const org = useOrg();
  const newest = useReleases(ref);
  const [shown, setShown] = useState(short ? 5 : 10);
  const all = useQuery({ ...queries.releases(ref, MAX), enabled: shown > 20 });
  const releases = (all.data ?? newest).slice(0, shown);
  const more = shown < newest.length || (!all.data && newest.length === 20);
  return (
    <section aria-labelledby="releases">
      <SectionHead
        id="releases"
        aside={newest[0] && `${newest[0].number} in all`}
      >
        Releases
      </SectionHead>
      <ReleasesTable
        serviceRef={ref}
        releases={releases}
        deploying={deploying}
      />
      {short
        ? more && (
            <div className="mt-2">
              <Link
                to="/$org/$project/$environment/$service/history"
                params={{ org, ...ref }}
                className="btn sm"
              >
                All releases and activity <kbd>G H</kbd>
              </Link>
            </div>
          )
        : more && (
            <div className="mt-2 flex items-center gap-3">
              <Act
                small
                cli={`dock release list -n ${MAX} -s ${ref.service} -e ${ref.environment}`}
                disabled={all.isFetching}
                onClick={() => setShown(shown < 20 ? 20 : MAX)}
              >
                {shown < 20 ? "Show more" : `Show up to ${MAX}`}
              </Act>
              <ErrorText error={all.error} />
            </div>
          )}
    </section>
  );
}
