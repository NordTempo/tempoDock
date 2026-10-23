import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";

// Updating Dock (ADR 0028). The status is read when the console opens
// (the daemon reuses its release check for 15 minutes) and kept current
// by update events, which carry everything but the release notes.

export type UpdateStatus = components["schemas"]["UpdateStatus"];

export const updateKey = ["update"] as const;

export const updateQuery = () =>
  queryOptions({
    queryKey: updateKey,
    queryFn: () => body(api.GET("/api/v1/update")),
  });

/** Patches an update event into the cached status, keeping the notes. */
export function applyUpdateEvent(
  queryClient: QueryClient,
  update: UpdateStatus,
) {
  queryClient.setQueryData<UpdateStatus>(updateKey, (old) =>
    old ? { ...update, releases: old.releases } : undefined,
  );
}
