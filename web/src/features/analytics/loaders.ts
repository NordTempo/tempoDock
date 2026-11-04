import { queryOptions } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";

// What analytics routes need before their views load: their search
// params and their loaders' queries. Route definitions are in the first
// bundle (only their components are split off), so this stays small and
// data.ts, which re-exports it, loads with the views.

/** The named ranges a dashboard offers, ending now. */
export const statsRanges = ["24h", "7d", "30d", "90d", "12m"] as const;
export type StatsRange = (typeof statsRanges)[number];

/** A filter as the API takes it: "dimension:op:value". */
export type Filter = {
  dimension: string;
  op: "is" | "is_not" | "contains";
  value: string;
};
export const filterParam = (f: Filter) => `${f.dimension}:${f.op}:${f.value}`;

/** Parses "dimension:op:value"; undefined for anything else. */
export function parseFilter(s: string): Filter | undefined {
  const [dimension, op, ...rest] = s.split(":");
  if (!dimension || (op !== "is" && op !== "is_not" && op !== "contains"))
    return;
  return { dimension, op, value: rest.join(":") };
}

export const analyticsKey = ["analytics"] as const;
const sitesKey = (project: string) => ["analytics", "sites", project] as const;

export const sitesQuery = (project: string) =>
  queryOptions({
    queryKey: sitesKey(project),
    queryFn: () =>
      list(
        api.GET("/api/v1/projects/{project}/sites", {
          params: { path: { project } },
        }),
      ),
  });

export const analyticsStatusQuery = () =>
  queryOptions({
    queryKey: ["analytics", "status"],
    queryFn: () => body(api.GET("/api/v1/analytics")),
  });

export const analyticsDefaultsQuery = () =>
  queryOptions({
    queryKey: ["analytics", "defaults"],
    queryFn: () => body(api.GET("/api/v1/analytics/defaults")),
  });

/** The search every analytics view shares: the site, range and filters. */
export type AnalyticsSearch = {
  site?: string;
  range?: StatsRange;
  f?: string[];
};

/** Reads the shared search from a URL's, dropping what isn't valid. */
export const analyticsSearch = (
  s: Record<string, unknown>,
): AnalyticsSearch => ({
  ...(typeof s.site === "string" && { site: s.site }),
  ...(statsRanges.includes(s.range as StatsRange) && {
    range: s.range as StatsRange,
  }),
  // One filter may come as a plain string (?f=country:is:NO), more as a list.
  ...((Array.isArray(s.f) || typeof s.f === "string") && {
    f: [s.f]
      .flat()
      .filter((f): f is string => typeof f === "string" && !!parseFilter(f)),
  }),
});

/** The vitals a view charts. */
export const vitals = ["lcp", "inp", "cls", "fcp", "ttfb"] as const;
export type Vital = (typeof vitals)[number];
