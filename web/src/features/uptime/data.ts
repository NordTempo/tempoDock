import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys, servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// A service's uptime checks (ADR 0055): URLs on its domains probed from
// another server. uptime.* audit events refetch the list (it is under the
// service's key); uptime events patch a check's state in place.

type Schema = components["schemas"]["UptimeCheck"];
/** A check, with the lists the API may send as null as arrays. */
export type UptimeCheck = Omit<Schema, "series" | "incidents"> & {
  series: NonNullable<Schema["series"]>;
  incidents: NonNullable<Schema["incidents"]>;
};
export type NewUptimeCheck = components["schemas"]["AddUptimeCheckRequest"];
type UptimeUpdate = components["schemas"]["UptimeUpdate"];
type UptimeCheckPatch = components["schemas"]["UpdateUptimeCheckRequest"];

const path =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/uptime-checks";

/** Under the service, so uptime.* audit events refetch it. */
export const uptimeKey = (ref: ServiceRef) => [...keys.service(ref), "uptime"];

export const uptimeChecks = (ref: ServiceRef) =>
  queryOptions({
    queryKey: uptimeKey(ref),
    queryFn: () =>
      list(api.GET(path, { params: { path: servicePath(ref) } })).then(
        (checks): UptimeCheck[] =>
          checks.map((c) => ({
            ...c,
            series: c.series ?? [],
            incidents: c.incidents ?? [],
          })),
      ),
  });

export const useAddUptimeCheck = (ref: ServiceRef) =>
  useAction(
    (req: NewUptimeCheck) =>
      body(api.POST(path, { params: { path: servicePath(ref) }, body: req })),
    () => [uptimeKey(ref), keys.overview(ref.project)],
  );

/** Checks every verified domain without a check (ADR 0059). */
export const useCheckDomains = (ref: ServiceRef) =>
  useAction(
    () =>
      list(
        api.POST(`${path}/domains` as const, {
          params: { path: servicePath(ref) },
        }),
      ),
    () => [uptimeKey(ref), keys.overview(ref.project)],
  );

/** Changes a check's interval or expected status in place. */
export const useUpdateUptimeCheck = (ref: ServiceRef) =>
  useAction(
    ({ id, ...patch }: { id: number } & UptimeCheckPatch) =>
      body(
        api.PATCH(`${path}/{id}` as const, {
          params: { path: { ...servicePath(ref), id } },
          body: patch,
        }),
      ),
    () => [uptimeKey(ref)],
  );

export const useRemoveUptimeCheck = (ref: ServiceRef) =>
  useAction(
    (id: number) =>
      done(
        api.DELETE(`${path}/{id}` as const, {
          params: { path: { ...servicePath(ref), id } },
        }),
      ),
    () => [uptimeKey(ref), keys.overview(ref.project)],
  );

/**
 * A check went up or down: patch its state and last probe into the list.
 * Returns the service's state as the overview sums it up: down while any
 * check is.
 */
export function applyUptimeEvent(
  queryClient: QueryClient,
  ref: ServiceRef,
  update: UptimeUpdate,
): UptimeUpdate["state"] {
  const checks = queryClient.setQueryData<UptimeCheck[]>(
    uptimeKey(ref),
    (list) =>
      list?.map((c) =>
        c.id === update.id
          ? {
              ...c,
              state: update.state,
              last_probe: update.last_probe ?? c.last_probe,
            }
          : c,
      ),
  );
  return update.state === "down" || checks?.some((c) => c.state === "down")
    ? "down"
    : update.state;
}

/** The `dock uptime add` line for a new check. */
export function addCli(ref: ServiceRef, req: NewUptimeCheck) {
  const flags = [
    req.expect_status && req.expect_status !== "200"
      ? `--status ${req.expect_status}`
      : "",
    req.interval_seconds && req.interval_seconds !== 60
      ? `--interval ${req.interval_seconds}s`
      : "",
    req.timeout_seconds && req.timeout_seconds !== 10
      ? `--timeout ${req.timeout_seconds}s`
      : "",
  ].filter(Boolean);
  return `dock uptime add ${req.url || "<url>"} ${flags.join(" ")}${flags.length ? " " : ""}-s ${ref.service} -e ${ref.environment}`;
}
