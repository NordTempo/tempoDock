import { api, body } from "../../api/client";
import { serviceWide, useAction } from "../../data/mutations";
import { servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { imageKeys } from "./data";

// Image updates (ADR 0074, amendment 2026-11-26): asking the registry now
// and turning automatic updates on or off. Apart from data.ts, which is
// in the initial bundle: only the screens that show them load these.

/**
 * Check now: asks the registry what the tag the service's live image
 * follows points at. Its scan events patch the pages that show it.
 */
export const useCheckImage = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/image-check",
          { params: { path: servicePath(ref) } },
        ),
      ),
    () => [],
    (_ref, scan) => [
      scan.base?.moved
        ? `${scan.base.image} has a newer image`
        : `${scan.base?.image ?? scan.image} is up to date`,
      "info",
    ],
  );

/** Turns a service's automatic image updates on or off; applies at once. */
export const useSetAutoUpdate = () =>
  useAction(
    ({ ref, on }: { ref: ServiceRef; on: boolean }) =>
      body(
        api.PUT(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/auto-update",
          { params: { path: servicePath(ref) }, body: { auto_update: on } },
        ),
      ),
    // The Images page shows which services update on their own.
    ({ ref }) => [...serviceWide(ref), imageKeys.all],
  );
