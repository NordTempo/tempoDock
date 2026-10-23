import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";

// The install's backup settings and the control plane's own backup
// (ADR 0019, 0021), for admins. backup.* audit events without a project
// (configure, control plane runs) refetch the settings (data/live.ts).

type Schemas = components["schemas"];
export type BackupSettings = Schemas["BackupSettings"];
export type ControlPlaneRun = Schemas["ControlPlaneBackupRun"];
export type BackupConfig = Schemas["ConfigureBackupRequest"];

export const backupSettingsKey = ["backup"] as const;

export const backupQueries = {
  settings: () =>
    queryOptions({
      queryKey: backupSettingsKey,
      queryFn: () => body(api.GET("/api/v1/backup")),
    }),
};

export const useBackupSettings = () =>
  useSuspenseQuery(backupQueries.settings()).data;

/**
 * The repository password, fetched only while `shown`. It lives in memory
 * only while on screen, like revealed variables.
 */
export const useRevealedPassword = (shown: boolean) =>
  useQuery({
    queryKey: ["backup-reveal"],
    queryFn: async () =>
      (
        await body(
          api.GET("/api/v1/backup", { params: { query: { reveal: true } } }),
        )
      ).password ?? "",
    enabled: shown,
    staleTime: 0,
    gcTime: 0,
  });

/**
 * Saves the fields set in the request; the rest stay. Resolves with the
 * settings, which carry the repository password only when this call
 * generated it.
 */
export function useConfigureBackups() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (req: BackupConfig) =>
      body(api.PUT("/api/v1/backup", { body: req })),
    onSuccess: ({ password: _, ...settings }) =>
      queryClient.setQueryData(backupSettingsKey, settings),
  });
}

/** Starts a control plane backup; its audit events say when it ended. */
export function useRunControlPlaneBackup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => body(api.POST("/api/v1/backup/control-plane")),
    onSuccess: (latest) =>
      queryClient.setQueryData<BackupSettings>(
        backupSettingsKey,
        (s) => s && { ...s, control_plane: { ...s.control_plane, latest } },
      ),
  });
}
