import { useQueries } from "@tanstack/react-query";
import { queries, serviceRef } from "../../data/queries";
import type { ReleaseSummary, ServiceView } from "../../data/types";

/** A release that only ran an earlier one again, not a new deploy. */
const rerun = (r: Pick<ReleaseSummary, "source">) =>
  r.source === "rollback" || r.source === "restart";

/**
 * The failed deploy each service's latest release hides, by service id:
 * after a deploy fails, a rollback or restart serves again and becomes
 * the latest release, so the overview's latest_release says nothing
 * failed. The failure stays here until a newer deploy (not a rollback or
 * restart) succeeds. A failed latest release is not included: the
 * overview shows that one itself. Release lists load only for services
 * whose latest release is a rollback or restart, so most pages fetch
 * nothing more.
 */
export function useHiddenFailures(services: readonly ServiceView[]) {
  const suspects = services.filter(
    (s) =>
      !s.deploying &&
      s.latest_release &&
      s.latest_release.status !== "failed" &&
      rerun(s.latest_release),
  );
  return useQueries({
    queries: suspects.map((s) => queries.releases(serviceRef(s.service))),
    combine: (results) => {
      const hidden = new Map<number, ReleaseSummary>();
      results.forEach((r, i) => {
        const service = suspects[i];
        // Newest first: the last release that was a new deploy.
        const deploy = r.data?.find((release) => !rerun(release));
        if (service && deploy?.status === "failed") {
          hidden.set(service.service.id, deploy);
        }
      });
      return hidden;
    },
  });
}
