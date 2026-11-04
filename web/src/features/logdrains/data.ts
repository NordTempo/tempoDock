import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { envPath, keys } from "../../data/queries";
import type { EnvRef } from "../../data/types";

// An environment's log drains (ADR 0046). Delivery counters change with
// every batch and have no live event, so the list is read when the page
// opens and after each change made here; nothing polls.

export type LogDrain = components["schemas"]["LogDrain"];
export type NewLogDrain = components["schemas"]["AddLogDrainRequest"];

const PATH = "/api/v1/projects/{project}/environments/{environment}/log-drains";

const drainsKey = (e: EnvRef) => [...keys.env(e), "log-drains"];

export const logDrainQueries = {
  list: (e: EnvRef) =>
    queryOptions({
      queryKey: drainsKey(e),
      queryFn: () => list(api.GET(PATH, { params: { path: envPath(e) } })),
    }),
};

export const useLogDrains = (e: EnvRef) =>
  useSuspenseQuery(logDrainQueries.list(e)).data;

const touched = (e: EnvRef) => [drainsKey(e), ["audit"]];

export const useAddLogDrain = (e: EnvRef) =>
  useAction(
    (input: NewLogDrain) =>
      body(api.POST(PATH, { params: { path: envPath(e) }, body: input })),
    () => touched(e),
  );

export const useRemoveLogDrain = (e: EnvRef) =>
  useAction(
    (id: number) =>
      done(
        api.DELETE(`${PATH}/{id}`, { params: { path: { ...envPath(e), id } } }),
      ),
    () => touched(e),
  );

export const useTestLogDrain = (e: EnvRef) =>
  useAction(
    (id: number) =>
      body(
        api.POST(`${PATH}/{id}/test`, {
          params: { path: { ...envPath(e), id } },
        }),
      ),
    () => touched(e),
  );
