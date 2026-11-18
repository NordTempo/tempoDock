import {
  queryOptions,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys, servicePath } from "../../data/queries";
import type { ServiceBackups, ServiceRef } from "../../data/types";
import { backupSettingsKey } from "./settings";

// A stateful service's volume snapshots (ADR 0045), and its backup runs'
// logs. The keys sit under the service, so snapshot.* audit events (which
// name the project and environment) refetch them through data/live.ts;
// nothing polls.

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

/**
 * What a backup or restore run's job printed (`dock backup log`): read
 * when View log opens. A finished run's log doesn't change, so it is
 * fetched once.
 */
export const backupLogQuery = (ref: ServiceRef, id: number) =>
  queryOptions({
    queryKey: [...keys.backups(ref), id, "log"],
    queryFn: () =>
      body(
        api.GET(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/backups/{id}/log",
          { params: { path: { ...servicePath(ref), id } } },
        ),
      ),
    staleTime: Number.POSITIVE_INFINITY,
  });

/** A failed snapshot's job output (`dock volume snapshot log`), read once. */
export const snapshotLogQuery = (ref: ServiceRef, id: number) =>
  queryOptions({
    queryKey: [...snapshotsKey(ref), id, "log"],
    queryFn: () =>
      body(
        api.GET(`${snapshotPath}/{snapshot}/log`, {
          params: { path: { ...servicePath(ref), snapshot: id } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
  });

/**
 * Points the environment's `${{from.KEY}}` references at `to` and applies
 * it (ADR 0061); with `dry_run`, only lists the services that would get a
 * release. `ref` is the service switched away from.
 */
export const useSwitchReferences = () =>
  useAction(
    ({
      ref,
      ...input
    }: { ref: ServiceRef } & Schemas["SwitchReferencesRequest"]) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/switch-references",
          { params: { path: servicePath(ref) }, body: input },
        ),
      ),
    ({ ref, dry_run }) =>
      dry_run ? [] : [keys.project(ref.project), ["audit"]],
  );

/**
 * Turns a database's point-in-time restore on or off, or sets its days
 * (ADR 0093; operators). Turning it on or off starts a restart release,
 * which the overview follows from its events; the answer replaces the
 * backups query.
 */
export function useSetPITR() {
  const queryClient = useQueryClient();
  return useAction(
    async ({
      ref,
      ...input
    }: { ref: ServiceRef } & Schemas["SetPITRRequest"]) => {
      const b = await body(
        api.PUT(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/backups/pitr",
          { params: { path: servicePath(ref) }, body: input },
        ),
      );
      const answer: ServiceBackups = { ...b, runs: b.runs ?? [] };
      queryClient.setQueryData(keys.backups(ref), answer);
      return answer;
    },
    ({ ref }) => [keys.overview(ref.project), ["audit"]],
  );
}

/**
 * Runs a template database's restore drill now (ADR 0096; operators): its
 * newest snapshot restored into a throwaway service, queried and removed.
 * It ends in the background; its audit event refetches the backups.
 */
export const useRunDrill = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/backups/drill",
          { params: { path: servicePath(ref) } },
        ),
      ),
    (ref) => [keys.backups(ref), ["audit"]],
  );

/**
 * Turns the weekly repository checks and restore drills on or off (ADR
 * 0096); the answer replaces the cached settings.
 */
export function useSetBackupChecks() {
  const queryClient = useQueryClient();
  return useAction(
    async (enabled: boolean) => {
      const answer = await body(
        api.PUT("/api/v1/backup/checks", { body: { enabled } }),
      );
      queryClient.setQueryData(backupSettingsKey, answer);
      return answer;
    },
    () => [],
  );
}

/**
 * Starts every repository's check now. The answer (the checks, running)
 * replaces the cached settings, and backup.* audit events refetch them as
 * each check ends.
 */
export function useRunBackupChecks() {
  const queryClient = useQueryClient();
  return useAction(
    async () => {
      const answer = await body(api.POST("/api/v1/backup/checks/run"));
      queryClient.setQueryData(backupSettingsKey, answer);
      return answer;
    },
    () => [],
  );
}
