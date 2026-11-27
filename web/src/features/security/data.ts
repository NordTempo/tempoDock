import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// Host checks (ADR 0109): each server's read-only security check of its
// host, for the console's Security page. Operators and platform viewers
// only. server.host_* audit events refetch it (data/live.ts); a server's
// daily check shows at the next load.

type Schemas = components["schemas"];
export type HostChecks = Schemas["HostChecks"];
export type HostCheck = Schemas["HostCheck"];

export const hostChecksKey = ["host-checks"] as const;

export const hostChecksQuery = () =>
  queryOptions({
    queryKey: hostChecksKey,
    queryFn: () => list(api.GET("/api/v1/host-checks")),
  });

export const useHostChecks = () => useSuspenseQuery(hostChecksQuery()).data;

/** A host check call: refetches the page and the activity log. */
const useHostAction = <Vars, Data>(mutationFn: (vars: Vars) => Promise<Data>) =>
  useAction(mutationFn, () => [hostChecksKey, ["audit"]]);

/** Check now: runs a server's checks and answers with the results. */
export const useCheckHost = () =>
  useHostAction((server: string) =>
    body(
      api.POST("/api/v1/servers/{server}/host-checks", {
        params: { path: { server } },
      }),
    ),
  );

/** Dismisses a finding with a reason; it stops counting and alerting. */
export const useDismissFinding = () =>
  useHostAction(
    ({
      server,
      finding,
      reason,
    }: {
      server: string;
      finding: string;
      reason: string;
    }) =>
      body(
        api.POST("/api/v1/servers/{server}/host-checks/dismiss", {
          params: { path: { server } },
          body: { finding, reason },
        }),
      ),
  );

/** Undoes a dismissal. */
export const useRestoreFinding = () =>
  useHostAction(({ server, finding }: { server: string; finding: string }) =>
    body(
      api.POST("/api/v1/servers/{server}/host-checks/restore", {
        params: { path: { server } },
        body: { finding },
      }),
    ),
  );

/** How bad a result is, for sorting across servers: worst first. */
export const rank = (c: HostCheck) =>
  c.status !== "finding"
    ? 0
    : c.severity === "critical"
      ? 3
      : c.severity === "warning"
        ? 2
        : 1;
