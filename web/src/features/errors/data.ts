import {
  queryOptions,
  useQuery,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// Error issues (ADR 0062): errors grouped by fingerprint from the browser
// (Analytics), the services' logs and 5xx spikes, for the org's Errors
// page and a service's Errors tab. A new or regressed issue raises an
// alert, and alert events refetch the list (data/live.ts); nothing polls.

type Schemas = components["schemas"];
export type ErrorIssue = Schemas["ErrorIssue"];
export type ErrorFacet = Schemas["ErrorFacet"];
export type ErrorState = ErrorIssue["state"];

/** What the list shows: one project, environment or service, or all. */
export type ErrorFilter = {
  project?: string;
  environment?: string;
  service?: string;
  /** Resolved issues too. */
  all?: boolean;
};

export const errorKeys = {
  all: ["errors"] as const,
  list: (f: ErrorFilter) => ["errors", "list", f] as const,
  issue: (id: number) => ["errors", "issue", id] as const,
};

export const errorsQuery = (f: ErrorFilter) =>
  queryOptions({
    queryKey: errorKeys.list(f),
    queryFn: () =>
      body(
        api.GET("/api/v1/errors", {
          params: {
            query: {
              project: f.project,
              environment: f.environment,
              service: f.service,
              state: f.all ? "all" : "open",
            },
          },
        }),
      ),
  });

export const useErrors = (f: ErrorFilter) =>
  useSuspenseQuery(errorsQuery(f)).data;

/** One issue with its latest sample, loaded when it is opened. */
export const useErrorIssue = (id: number | undefined) =>
  useQuery({
    queryKey: errorKeys.issue(id ?? 0),
    queryFn: () =>
      body(
        api.GET("/api/v1/errors/{id}", {
          params: { path: { id: id ?? 0 } },
        }),
      ),
    enabled: id !== undefined,
  });

export const useResolveError = () =>
  useAction(
    (id: number) =>
      body(
        api.POST("/api/v1/errors/{id}/resolve", { params: { path: { id } } }),
      ),
    () => [errorKeys.all, ["audit"]],
    (_id, issue) => `Resolved ${issue.title}`,
  );

const order: Record<ErrorState, number> = {
  new: 0,
  regressed: 1,
  ongoing: 2,
  resolved: 3,
};

/** New and regressed first, then the most recently seen. */
export const byState = (a: ErrorIssue, b: ErrorIssue) =>
  order[a.state] - order[b.state] ||
  Date.parse(b.last_seen) - Date.parse(a.last_seen);
