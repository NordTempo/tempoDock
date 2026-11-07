import {
  type QueryClient,
  queryOptions,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ApiError, api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { envPath, keys } from "../../data/queries";
import type { EnvRef } from "../../data/types";
import { elapsed } from "../../ui/format";

// An environment's move to another server (ADRs 0053, 0063): the running move or
// the latest one, null when it never moved. environment_move events carry
// the whole move and set it here (data/live.ts); nothing polls. The key
// sits outside the environment's, so the audits of other changes in it
// don't refetch the move.

export type EnvironmentMove = components["schemas"]["EnvironmentMove"];
export type MoveStep = EnvironmentMove["step"];

const moveKey = (e: EnvRef) => ["move", e.project, e.environment];

const movePath =
  "/api/v1/projects/{project}/environments/{environment}/move" as const;

export const moveQuery = (e: EnvRef) =>
  queryOptions({
    queryKey: moveKey(e),
    queryFn: async (): Promise<EnvironmentMove | null> => {
      try {
        return await body(api.GET(movePath, { params: { path: envPath(e) } }));
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    staleTime: Number.POSITIVE_INFINITY,
  });

/** The environment's running or latest move; null while loading or never. */
export const useMove = (e: EnvRef) => useQuery(moveQuery(e)).data ?? null;

export const moveRunning = (m: EnvironmentMove | null) =>
  m?.status === "running" || m?.status === "rolling_back";

/** The hold's length, "2.8s", or "" while it lasts or never was. */
export function heldFor(move: EnvironmentMove) {
  const { downtime_from: from, downtime_to: to } = move;
  if (!from || !to) return "";
  const ms = Date.parse(to) - Date.parse(from);
  return ms < 10_000 ? `${(ms / 1000).toFixed(1)}s` : elapsed(ms);
}

/**
 * Starts a move; it runs in the background. The answer is the move at its
 * first step: events that already moved it further win.
 */
export function useMoveEnvironment() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ env, to }: { env: EnvRef; to: string }) => {
      const m = await body(
        api.POST(movePath, { params: { path: envPath(env) }, body: { to } }),
      );
      queryClient.setQueryData<EnvironmentMove | null>(moveKey(env), (old) =>
        old?.id === m.id ? old : m,
      );
      return m;
    },
    () => [["audit"]],
  );
}

/**
 * An environment_move event: the move replaces the cached one. When it
 * starts, the project overview refetches for its moving_to; when it ends,
 * the environment is on another server (or its stateful services started
 * again on the old one), so the project's queries and the server list
 * refetch.
 */
export function applyMoveEvent(
  queryClient: QueryClient,
  move: EnvironmentMove,
) {
  const ref = { project: move.project, environment: move.environment };
  const before = queryClient.getQueryData<EnvironmentMove | null>(moveKey(ref));
  queryClient.setQueryData(moveKey(ref), move);
  const ended = move.status === "succeeded" || move.status === "failed";
  // Started, or ended: the overview's moving_to changes with it.
  const started = !ended && !moveRunning(before ?? null);
  if (started || (ended && before?.status !== move.status)) {
    queryClient.invalidateQueries({ queryKey: keys.project(move.project) });
    queryClient.invalidateQueries({ queryKey: keys.servers });
  }
}
