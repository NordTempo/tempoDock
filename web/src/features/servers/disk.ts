import { queryOptions, useQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// Where a server's disk goes (ADR 0035). The daemon answers with its last
// reading at once (only the first read waits) and, when it is old, reads
// again in the background: the new one arrives as a disk event
// (data/live.ts patches this query). It is its own query the page doesn't
// wait for.

export type ServerDisk = components["schemas"]["ServerDisk"];
export type DiskPart = components["schemas"]["DiskPart"];
export type DiskPrune = components["schemas"]["DiskPrune"];
export type PruneTarget = DiskPrune["target"];

export const diskKey = (server: string) => ["servers", server, "disk"] as const;

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

/**
 * Prunes a server now: the given targets, or without any the safe ones
 * (ADR 0035 amendment 2). The disk is read again afterwards.
 */
export const usePruneServer = (server: string) =>
  useAction(
    (targets: PruneTarget[]) =>
      body(
        api.POST("/api/v1/servers/{server}/prune", {
          params: { path: { server } },
          body: { targets },
        }),
      ),
    () => [diskKey(server), ["audit"]],
  );
