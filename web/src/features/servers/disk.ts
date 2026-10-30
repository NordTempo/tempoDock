import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";

// Where a server's disk goes (ADR 0035), read when the server page opens:
// Docker sizes every image and volume for it, which takes seconds, so it
// is its own query the page doesn't wait for, kept a minute.

export type ServerDisk = components["schemas"]["ServerDisk"];
export type DiskPart = components["schemas"]["DiskPart"];

const diskKey = (server: string) => ["servers", server, "disk"] as const;

export const diskQuery = (server: string) =>
  queryOptions({
    queryKey: diskKey(server),
    queryFn: () =>
      body(
        api.GET("/api/v1/servers/{server}/disk", {
          params: { path: { server } },
        }),
      ),
    staleTime: 60_000,
  });

export const useServerDisk = (server: string) => useQuery(diskQuery(server));

/** Prunes a server now; the disk is read again afterwards. */
export function usePruneServer(server: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      body(
        api.POST("/api/v1/servers/{server}/prune", {
          params: { path: { server } },
        }),
      ),
    onSettled: () =>
      Promise.all(
        [diskKey(server), ["audit"]].map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}
