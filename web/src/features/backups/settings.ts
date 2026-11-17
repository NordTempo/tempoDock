import {
  queryOptions,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// The install's backup settings and the control plane's own backup
// (ADR 0019, ADR 0021), for the console's Backups page. backup.* audit
// events without a project refetch them (data/live.ts); nothing polls.

type Schemas = components["schemas"];
export type BackupSettings = Schemas["BackupSettings"];
export type ConfigureBackup = Schemas["ConfigureBackupRequest"];

export const backupSettingsKey = ["backup-settings"] as const;

export const backupSettingsQuery = () =>
  queryOptions({
    queryKey: backupSettingsKey,
    queryFn: () => body(api.GET("/api/v1/backup")),
  });

export const useBackupSettings = () =>
  useSuspenseQuery(backupSettingsQuery()).data;

/** Saves the fields set; the answer replaces the cached settings. */
export function useConfigureBackups() {
  const queryClient = useQueryClient();
  return useAction(
    async (input: ConfigureBackup) => {
      const answer = await body(api.PUT("/api/v1/backup", { body: input }));
      queryClient.setQueryData(backupSettingsKey, answer);
      return answer;
    },
    () => [],
  );
}

/** Starts a control plane backup; its audit events refetch the settings. */
export const useRunControlPlaneBackup = () =>
  useAction(
    () => body(api.POST("/api/v1/backup/control-plane")),
    () => [backupSettingsKey, ["audit"]],
  );

/** Fetches the repository password once; it is never cached. */
export const useRevealBackupPassword = () =>
  useAction(
    async () =>
      (
        await body(
          api.GET("/api/v1/backup", { params: { query: { reveal: true } } }),
        )
      ).password ?? "",
    () => [["audit"]],
  );

export type MoveBackups = Schemas["MoveBackupsRequest"];

/**
 * Starts moving the backups to a storage connection; the answer (moving)
 * replaces the cached settings, and backup.* audit events refetch them
 * as the move goes on and ends.
 */
export function useMoveBackups() {
  const queryClient = useQueryClient();
  return useAction(
    async (input: MoveBackups) => {
      const answer = await body(
        api.POST("/api/v1/backup/move", { body: input }),
      );
      queryClient.setQueryData(backupSettingsKey, answer);
      return answer;
    },
    () => [],
  );
}
