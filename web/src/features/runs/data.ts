import {
  type QueryClient,
  queryOptions,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// One-off commands in a service (ADR 0048). The list stays live through
// `run` events (applyRunEvent), which carry no output; the Run tab reads a
// selected run's output once the list says it ended, as finished runs
// never change. Nothing polls.

export type CommandRun = components["schemas"]["CommandRun"];
export type RunRequest = components["schemas"]["RunCommandRequest"];
export type RunVolumes = NonNullable<RunRequest["volumes"]>;

/** Runs the API keeps per service; the list is never longer. */
const KEEP = 50;

// Outside the project tree: the service.run audit event at a run's end
// must not refetch what its run event already patched.
export const runKeys = {
  list: (ref: ServiceRef) => [
    "runs",
    ref.project,
    ref.environment,
    ref.service,
  ],
  one: (ref: ServiceRef, id: number) => [...runKeys.list(ref), id],
};

export const runQueries = {
  /** The latest runs, newest first, without output. Scope read. */
  list: (ref: ServiceRef) =>
    queryOptions({
      queryKey: runKeys.list(ref),
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/runs",
            { params: { path: servicePath(ref) } },
          ),
        ),
    }),
  /** A finished run with its output's tail. Scope deploy. */
  one: (ref: ServiceRef, id: number) =>
    queryOptions({
      queryKey: runKeys.one(ref, id),
      queryFn: () =>
        body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/runs/{id}",
            { params: { path: { ...servicePath(ref), id } } },
          ),
        ),
      staleTime: Number.POSITIVE_INFINITY,
    }),
};

export const isRunning = (r: Pick<CommandRun, "status">) =>
  r.status === "running";

/**
 * The list with run in it, newest first. A finished run is final, so a
 * running copy arriving late (the POST's answer after the end event)
 * never replaces it.
 */
function upsertRun(runs: CommandRun[] | undefined, run: CommandRun) {
  if (!runs) return runs;
  const old = runs.find((r) => r.id === run.id);
  if (old && !isRunning(old) && isRunning(run)) return runs;
  const rest = runs.filter((r) => r.id !== run.id);
  return [run, ...rest].sort((a, b) => b.id - a.id).slice(0, KEEP);
}

const refOf = (run: CommandRun): ServiceRef => ({
  project: run.project,
  environment: run.environment,
  service: run.service,
});

/**
 * A run started or ended (from here, the CLI or an agent), or a POST
 * answered with one: patch the service's cached list.
 */
export function applyRunEvent(queryClient: QueryClient, run: CommandRun) {
  queryClient.setQueryData<CommandRun[]>(runKeys.list(refOf(run)), (runs) =>
    upsertRun(runs, run),
  );
}

/**
 * Runs a command in the background. Its answer, the run, goes into the
 * list; run events take it from there, so nothing is refetched.
 */
export function useRunCommand() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ ref, req }: { ref: ServiceRef; req: RunRequest }) => {
      const run = await body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/runs",
          { params: { path: servicePath(ref) }, body: req },
        ),
      );
      applyRunEvent(queryClient, run);
      return run;
    },
    () => [],
  );
}
