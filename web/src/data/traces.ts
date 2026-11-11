import { queryOptions } from "@tanstack/react-query";
import { api, body } from "../api/client";
import type { components } from "../api/schema";
import { servicePath } from "./queries";
import type { ServiceRef } from "./types";

// Traces (ADR 0075): a service's list and one trace, read when a view
// opens. Spans arrive in batches from SDKs, so nothing here is live: a
// list is fresh for half a minute, and a trace, once read, doesn't change
// unless spans were still on their way.

type Schemas = components["schemas"];
export type TraceSummary = Schemas["TraceSummary"];
export type TraceSpan = Schemas["TraceSpan"];
/** The API's list and trace, their arrays never null. */
export type TraceList = Schemas["TraceList"] & { traces: TraceSummary[] };
export type Trace = Schemas["Trace"] & { spans: TraceSpan[] };
export type TraceRange = TraceList["range"];

export const traceRanges = [
  "1h",
  "24h",
  "7d",
] as const satisfies readonly TraceRange[];

/** What a list shows; the API's query parameters. */
export type TraceFilter = {
  range?: TraceRange;
  errors?: boolean;
  slowest?: boolean;
  route?: string;
  /** One release, by number. */
  release?: number;
};

export const traceKeys = {
  list: (s: ServiceRef, f: TraceFilter) =>
    [
      "project",
      s.project,
      "env",
      s.environment,
      "service",
      s.service,
      "traces",
      f,
    ] as const,
  trace: (s: ServiceRef, id: string) =>
    [
      "project",
      s.project,
      "env",
      s.environment,
      "service",
      s.service,
      "trace",
      id,
    ] as const,
};

export const tracesQuery = (ref: ServiceRef, f: TraceFilter) =>
  queryOptions({
    queryKey: traceKeys.list(ref, f),
    queryFn: async (): Promise<TraceList> => {
      const l = await body(
        api.GET(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/traces",
          {
            params: {
              path: servicePath(ref),
              query: {
                range: f.range,
                errors: f.errors || undefined,
                slowest: f.slowest || undefined,
                route: f.route || undefined,
                release: f.release || undefined,
              },
            },
          },
        ),
      );
      return { ...l, traces: l.traces ?? [] };
    },
    staleTime: 30_000,
  });

/** One trace; at (a list row's start) makes finding it quick. */
export const traceQuery = (ref: ServiceRef, id: string, at?: string) =>
  queryOptions({
    queryKey: traceKeys.trace(ref, id),
    queryFn: async (): Promise<Trace> => {
      const t = await body(
        api.GET(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/traces/{trace}",
          {
            params: { path: { ...servicePath(ref), trace: id }, query: { at } },
          },
        ),
      );
      return { ...t, spans: t.spans ?? [] };
    },
    staleTime: 60_000,
  });
