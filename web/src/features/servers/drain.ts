import {
  type QueryClient,
  queryOptions,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ApiError, api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";

// A server's drain (ADR 0062): its running or latest drain with the ports
// it forwards, null when it never drained and forwards nothing.
// server_drain events carry the whole drain and set it here
// (data/live.ts); nothing polls. The server list carries each server's
// drain progress for the console's strip, patched by server events.

export type ServerDrain = components["schemas"]["ServerDrain"];
export type DrainItem = NonNullable<ServerDrain["items"]>[number];
export type DrainRequest = components["schemas"]["DrainServerRequest"];

export const drainKey = (server: string) => ["drain", server];

const drainPath = "/api/v1/servers/{server}/drain" as const;

export const drainQuery = (server: string) =>
  queryOptions({
    queryKey: drainKey(server),
    queryFn: async (): Promise<ServerDrain | null> => {
      try {
        return await body(api.GET(drainPath, { params: { path: { server } } }));
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    staleTime: Number.POSITIVE_INFINITY,
  });

/** The server's running or latest drain; null while loading or never. */
export const useDrain = (server: string) =>
  useQuery(drainQuery(server)).data ?? null;

export const drainOpen = (d: ServerDrain | null) =>
  d?.status === "running" || d?.status === "paused";

/**
 * The plan of a drain: each environment, its target, its data and what
 * keeps it from moving. Changes nothing, so it touches no cache.
 */
export function usePlanDrain() {
  return useAction(
    ({ server, req }: { server: string; req: DrainRequest }) =>
      body(
        api.POST(drainPath, {
          params: { path: { server } },
          body: { ...req, dry_run: true },
        }),
      ),
    () => [],
  );
}

/** Sets the drain an action answered with, unless an event was newer. */
function useSetDrain() {
  const queryClient = useQueryClient();
  return (d: ServerDrain) => {
    queryClient.setQueryData<ServerDrain | null>(drainKey(d.server), d);
    return d;
  };
}

/** Starts a drain; it runs in the background. */
export function useStartDrain() {
  const set = useSetDrain();
  return useAction(
    async ({ server, req }: { server: string; req: DrainRequest }) =>
      set(
        await body(
          api.POST(drainPath, {
            params: { path: { server } },
            body: { ...req, dry_run: false },
          }),
        ),
      ),
    ({ server }) => [keys.servers, keys.server(server), ["audit"]],
    (vars) => [`Draining ${vars.server}`, "info"],
  );
}

/** Goes on with a paused drain, trying the failed one again or skipping it. */
export function useResumeDrain() {
  const set = useSetDrain();
  return useAction(
    async ({ server, skip }: { server: string; skip: boolean }) =>
      set(
        await body(
          api.POST(`${drainPath}/resume`, {
            params: { path: { server } },
            body: { skip },
          }),
        ),
      ),
    () => [["audit"]],
  );
}

/** Stops a drain after the move that runs. */
export function useCancelDrain() {
  const set = useSetDrain();
  return useAction(
    async ({ server }: { server: string }) =>
      set(
        await body(
          api.POST(`${drainPath}/cancel`, { params: { path: { server } } }),
        ),
      ),
    () => [["audit"]],
    (vars) => `The drain of ${vars.server} stops after the move that runs`,
  );
}

/**
 * A server_drain event: the drain replaces the cached one. When it ends,
 * environments are on other servers, so the server's page and the list
 * refetch.
 */
export function applyDrainEvent(queryClient: QueryClient, drain: ServerDrain) {
  const before = queryClient.getQueryData<ServerDrain | null>(
    drainKey(drain.server),
  );
  queryClient.setQueryData(drainKey(drain.server), drain);
  const moved = (d: ServerDrain | null | undefined) =>
    d?.items?.filter((i) => i.status === "moved").length ?? 0;
  if (moved(drain) !== moved(before) || drain.status !== before?.status) {
    queryClient.invalidateQueries({ queryKey: keys.server(drain.server) });
  }
}
