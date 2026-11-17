import {
  type QueryClient,
  queryOptions,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// Updating Dock (ADR 0028). The status is read when the console opens
// (the daemon reuses its release check for 15 minutes) and kept current
// by update events, which carry everything but the release notes.

export type UpdateStatus = components["schemas"]["UpdateStatus"];
export type UpdateServer = components["schemas"]["UpdateServer"];

export const updateKey = ["update"] as const;

export const updateQuery = () =>
  queryOptions({
    queryKey: updateKey,
    queryFn: () => body(api.GET("/api/v1/update")),
  });

/** The status, for the Updates page (its route loads it first). */
export const useUpdateStatus = () => useSuspenseQuery(updateQuery()).data;

/**
 * The status where it is a side note (the sidebar, the overview): never
 * holds the page up while the daemon asks the release source.
 */
export const useUpdateNote = () => useQuery(updateQuery()).data;

/** Checks the release source now instead of reusing the last check. */
export function useCheckNow() {
  const queryClient = useQueryClient();
  return useAction(
    async () => {
      const status = await body(
        api.GET("/api/v1/update", { params: { query: { refresh: true } } }),
      );
      queryClient.setQueryData(updateKey, status);
      return status;
    },
    () => [],
  );
}

/** Starts an update to version (the newest release when left out). */
export const useStartUpdate = () =>
  useAction(
    (version?: string) =>
      body(api.POST("/api/v1/update/apply", { body: { version } })),
    () => [updateKey, ["audit"]],
  );

/** Patches an update event into the cached status, keeping the notes. */
export function applyUpdateEvent(
  queryClient: QueryClient,
  update: UpdateStatus,
) {
  queryClient.setQueryData<UpdateStatus>(updateKey, (old) =>
    old ? { ...update, releases: old.releases } : undefined,
  );
}
