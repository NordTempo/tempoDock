import { api, body } from "../../api/client";
import { serviceWide, useAction } from "../../data/mutations";
import { servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// Stopping and starting a service (ADR 0050), for the service header's
// actions. Live events patch the status as it changes; the audit event
// refetches the overview, which carries stopped_at and stopped_by.

/** Stops the service: its domains answer 503 and its container is removed. */
export const useStopService = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/stop",
          { params: { path: servicePath(ref) } },
        ),
      ),
    serviceWide,
    (ref) => `Stopped ${ref.service}`,
  );

/** Starts a stopped service: a copy of its active release, health checked. */
export const useStartService = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/start",
          { params: { path: servicePath(ref) } },
        ),
      ),
    serviceWide,
    (ref) => `Starting ${ref.service}`,
  );

/**
 * Restarts the service: a new release with the active one's image and
 * config. The notice says it started; the header says when it serves.
 */
export const useRestartService = () =>
  useAction(
    ({ ref }: { ref: ServiceRef; stateful: boolean }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/restart",
          { params: { path: servicePath(ref) } },
        ),
      ),
    ({ ref }) => serviceWide(ref),
    ({ ref, stateful }, r) => [
      stateful
        ? `Restarting ${ref.service}: it stops, then r${r.number} starts`
        : `Restarting ${ref.service}: r${r.number} starts and takes over`,
      "info",
    ],
  );
