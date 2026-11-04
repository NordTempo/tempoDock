import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys, servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// A stateful service's volume snapshots (ADR 0045). The key sits under the
// service, so snapshot.* audit events (which name the project and
// environment) refetch it through data/live.ts; nothing polls.

type Schemas = components["schemas"];
export type Snapshot = Schemas["Snapshot"];
export type SnapshotRestore = Schemas["SnapshotRestore"];

const snapshotsKey = (ref: ServiceRef) => [...keys.service(ref), "snapshots"];

const snapshotPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/snapshots" as const;

export const snapshotsQuery = (ref: ServiceRef) =>
  queryOptions({
    queryKey: snapshotsKey(ref),
    queryFn: async () => {
      const s = await body(
        api.GET(snapshotPath, { params: { path: servicePath(ref) } }),
      );
      return { ...s, snapshots: s.snapshots ?? [], restores: s.restores ?? [] };
    },
  });

export const useSnapshots = (ref: ServiceRef) =>
  useSuspenseQuery(snapshotsQuery(ref)).data;

const touches = ({ ref }: { ref: ServiceRef }) => [
  snapshotsKey(ref),
  ["audit"],
];

/** Takes a snapshot now; it finishes in the background. */
export const useTakeSnapshot = () =>
  useAction(
    ({ ref, note }: { ref: ServiceRef; note: string }) =>
      body(
        api.POST(snapshotPath, {
          params: { path: servicePath(ref) },
          body: note ? { note } : {},
        }),
      ),
    touches,
  );

/**
 * Restores a snapshot into a new service (`as`) or in place. Resolves at
 * once with the restore and the new service; it finishes in the background.
 */
export const useRestoreSnapshot = () =>
  useAction(
    ({
      ref,
      id,
      ...input
    }: { ref: ServiceRef; id: number } & Schemas["RestoreSnapshotRequest"]) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/snapshots/{snapshot}/restore",
          {
            params: { path: { ...servicePath(ref), snapshot: id } },
            body: input,
          },
        ),
      ),
    ({ ref }) => [...touches({ ref }), keys.env(ref)],
  );

export const useDeleteSnapshot = () =>
  useAction(
    ({ ref, id }: { ref: ServiceRef; id: number }) =>
      done(
        api.DELETE(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/snapshots/{snapshot}",
          {
            params: { path: { ...servicePath(ref), snapshot: id } },
          },
        ),
      ),
    touches,
  );
