import {
  type QueryClient,
  queryOptions,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { servicePath } from "../../data/queries";
import type { LiveEvent, ServiceRef } from "../../data/types";
import { type CommandRun, upsertRun } from "../runs/data";

// A service's scheduled jobs (ADR 0052). The list and each job's history
// stay live through events: `job` events (set, on or off, removed) and
// `run` events of runs with a job. Mutations patch the cache with their
// answer; nothing polls or refetches.

export type Job = components["schemas"]["Job"];
export type SetJobRequest = components["schemas"]["SetJobRequest"];

/** Runs the API keeps per job. */
const KEEP = 20;

// Outside the project tree, like runKeys: job.* audit events must not
// refetch what job and run events already patched.
export const jobKeys = {
  list: (ref: ServiceRef) => [
    "jobs",
    ref.project,
    ref.environment,
    ref.service,
  ],
  runs: (ref: ServiceRef, job: string) => [...jobKeys.list(ref), job, "runs"],
};

const jobsPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/jobs/{job}" as const;

const jobPath = (ref: ServiceRef, job: string) => ({
  params: { path: { ...servicePath(ref), job } },
});

export const jobQueries = {
  /** The jobs by name, each with its next time and latest run. Scope read. */
  list: (ref: ServiceRef) =>
    queryOptions({
      queryKey: jobKeys.list(ref),
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/jobs",
            { params: { path: servicePath(ref) } },
          ),
        ),
    }),
  /** A job's latest runs, newest first, without output. Scope read. */
  runs: (ref: ServiceRef, job: string) =>
    queryOptions({
      queryKey: jobKeys.runs(ref, job),
      queryFn: () => list(api.GET(`${jobsPath}/runs`, jobPath(ref, job))),
    }),
};

/**
 * The list with job in it, by name. Job answers and events carry no
 * last_run (only the list does), so the cached one stays.
 */
function upsertJob(jobs: Job[] | undefined, job: Job) {
  if (!jobs) return jobs;
  const old = jobs.find((j) => j.name === job.name);
  const merged = { ...job, last_run: job.last_run ?? old?.last_run };
  return [...jobs.filter((j) => j.name !== job.name), merged].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
}

function removeJob(queryClient: QueryClient, ref: ServiceRef, name: string) {
  queryClient.setQueryData<Job[]>(jobKeys.list(ref), (jobs) =>
    jobs?.filter((j) => j.name !== name),
  );
  queryClient.removeQueries({ queryKey: jobKeys.runs(ref, name) });
}

const refOf = (e: {
  project?: string;
  environment?: string;
  service?: string;
}): ServiceRef | null =>
  e.project && e.environment && e.service
    ? { project: e.project, environment: e.environment, service: e.service }
    : null;

/**
 * A job was set, turned on or off, or removed (Message "removed", only
 * its name), from here, the CLI, an agent or a deploy's dock.toml.
 */
export function applyJobEvent(queryClient: QueryClient, event: LiveEvent) {
  const ref = refOf(event);
  const job = event.job;
  if (!ref || !job) return;
  if (event.message === "removed") return removeJob(queryClient, ref, job.name);
  queryClient.setQueryData<Job[]>(jobKeys.list(ref), (jobs) =>
    upsertJob(jobs, job),
  );
}

/**
 * A job's run started or ended (scheduled, skipped or run now), or Run now
 * answered: patch its history and its job's last run. Runs without a job
 * belong to the Run tab (applyRunEvent).
 */
export function applyJobRunEvent(queryClient: QueryClient, run: CommandRun) {
  const ref = refOf(run);
  const name = run.job;
  if (!ref || !name) return;
  queryClient.setQueryData<CommandRun[]>(jobKeys.runs(ref, name), (runs) =>
    upsertRun(runs, run, KEEP),
  );
  queryClient.setQueryData<Job[]>(jobKeys.list(ref), (jobs) =>
    jobs?.map((j) => {
      if (j.name !== name) return j;
      const last = j.last_run;
      // An older run, or a late running copy of one that ended, is stale.
      if (
        last &&
        (last.id > run.id || (last.id === run.id && last.status !== "running"))
      )
        return j;
      return { ...j, last_run: run };
    }),
  );
}

/** Creates or replaces a job; its answer goes into the list. Scope deploy. */
export function useSetJob() {
  const queryClient = useQueryClient();
  return useAction(
    async ({
      ref,
      name,
      req,
    }: {
      ref: ServiceRef;
      name: string;
      req: SetJobRequest;
    }) => {
      const job = await body(
        api.PUT(jobsPath, { ...jobPath(ref, name), body: req }),
      );
      queryClient.setQueryData<Job[]>(jobKeys.list(ref), (jobs) =>
        upsertJob(jobs, job),
      );
      return job;
    },
    () => [],
  );
}

/** Turns a job on or off, dock.toml's too. Scope deploy. */
export function useSetJobEnabled() {
  const queryClient = useQueryClient();
  return useAction(
    async ({
      ref,
      name,
      enabled,
    }: {
      ref: ServiceRef;
      name: string;
      enabled: boolean;
    }) => {
      const job = await body(
        api.PUT(`${jobsPath}/enabled`, {
          ...jobPath(ref, name),
          body: { enabled },
        }),
      );
      queryClient.setQueryData<Job[]>(jobKeys.list(ref), (jobs) =>
        upsertJob(jobs, job),
      );
      return job;
    },
    () => [],
  );
}

/** Removes a job and its history. Scope deploy. */
export function useRemoveJob() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ ref, name }: { ref: ServiceRef; name: string }) => {
      await done(api.DELETE(jobsPath, jobPath(ref, name)));
      removeJob(queryClient, ref, name);
    },
    () => [],
  );
}

/** Runs a job now; the run, running, goes into its history. Scope deploy. */
export function useRunJob() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ ref, name }: { ref: ServiceRef; name: string }) => {
      const run = await body(api.POST(`${jobsPath}/runs`, jobPath(ref, name)));
      applyJobRunEvent(queryClient, { ...run, job: run.job || name });
      return run;
    },
    () => [],
  );
}
