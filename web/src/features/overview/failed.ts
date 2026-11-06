import type { ReleaseSummary, ServiceView } from "../../data/types";

/**
 * The failed deploy each service's latest release hides, by service id:
 * after a deploy fails, a rollback or restart serves again and becomes
 * the latest release, so latest_release says nothing failed. The
 * overview's last_failed_deploy keeps it until a newer deploy succeeds.
 * A failed latest release is not included (the overview shows that one
 * itself), nor one while a deploy runs. No release lists are fetched.
 */
export function hiddenFailures(services: readonly ServiceView[]) {
  const hidden = new Map<number, ReleaseSummary>();
  for (const s of services) {
    const failed = s.last_failed_deploy;
    if (failed && !s.deploying && failed.number !== s.latest_release?.number) {
      hidden.set(s.service.id, failed);
    }
  }
  return hidden;
}
