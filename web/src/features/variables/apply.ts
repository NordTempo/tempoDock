import type { PendingChanges, ServiceView } from "../../data/types";

/**
 * Services that get a release when an environment's staged changes are
 * applied, as the API reports them (dependents through references too).
 */
export const affectedServices = (
  changes: PendingChanges,
  services: ServiceView[],
) =>
  changes.affected.flatMap((name) => {
    const service = services.find((s) => s.service.name === name);
    return service ? [service] : [];
  });

/** The release a service gets next: "web r62". */
export const nextRelease = (s: ServiceView) =>
  `${s.service.name} r${(s.latest_release?.number ?? 0) + 1}`;
