import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { redirect } from "@tanstack/react-router";
import { api, body, list } from "../../api/client";
import { remembered } from "../../data/remembered";

// What analytics routes need before their views load: their search
// params and their loaders' queries. Route definitions are in the first
// bundle (only their components are split off), so this stays small and
// data.ts, which re-exports it, loads with the views.

/** The named ranges a dashboard offers, ending now. */
export const statsRanges = ["24h", "7d", "30d", "90d", "12m"] as const;
export type StatsRange = (typeof statsRanges)[number];

/**
 * A site's range last picked on this browser, 30d until one is: the
 * Analytics area's site pages and a service's Visitors view share it.
 */
export const siteRange = remembered("analytics.site.range", statsRanges, "30d");

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

/** The ranges the organization's site list takes. */
export const orgSitesRanges = ["24h", "7d", "30d"] as const;

/** The site list's range last picked on this browser, 24h until one is. */
export const sitesRange = remembered("analytics.range", orgSitesRanges, "24h");
export type OrgSitesRange = (typeof orgSitesRanges)[number];

/**
 * The organization's sites with their numbers over a range, the range
 * before it and a series: the Analytics area's list, and (over 24h) the
 * service Overview's visitors line.
 */
export const orgSitesQuery = (range: OrgSitesRange = "24h") =>
  queryOptions({
    queryKey: [...orgSitesKey, range],
    queryFn: () =>
      list(api.GET("/api/v1/sites", { params: { query: { range } } })),
  });
export const orgSitesKey = ["analytics", "org-sites"] as const;

export const analyticsStatusQuery = () =>
  queryOptions({
    queryKey: ["analytics", "status"],
    queryFn: () => body(api.GET("/api/v1/analytics")),
  });

/** Tracing over OTLP and its trace store, for operators. */
export const tracingStatusQuery = () =>
  queryOptions({
    queryKey: ["analytics", "tracing"],
    queryFn: () => body(api.GET("/api/v1/tracing")),
  });

/** The org's defaults for new sites, over the platform's. */
export const orgAnalyticsDefaultsQuery = () =>
  queryOptions({
    queryKey: ["analytics", "defaults", "org"],
    queryFn: () => body(api.GET("/api/v1/org/analytics/defaults")),
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

/**
 * Redirects an old service analytics URL (…/web/analytics/vitals?site=…)
 * to its site's page in the Analytics area (ADR 0059): the site in its
 * search, or the first the service's hostnames count for; the area's list
 * without one. rest is the view below the site: "", "vitals", "funnels",
 * "replays" or "replays/<id>".
 */
export async function siteRedirect(
  queryClient: QueryClient,
  ref: { org: string; project: string; environment: string; service: string },
  search: Record<string, unknown>,
  rest: string,
): Promise<never> {
  const sites = await queryClient.ensureQueryData(sitesQuery(ref.project));
  const { site: picked, ...kept } = search;
  const site =
    sites.find((s) => s.name === picked) ??
    sites.find(
      (s) =>
        !s.archived &&
        s.environment === ref.environment &&
        (s.hostnames ?? []).some((h) => h.service === ref.service),
    );
  if (!site)
    throw redirect({ to: "/$org/analytics", params: { org: ref.org } });
  const params = { org: ref.org, project: ref.project, site: site.name };
  const [view, replay] = rest.split("/");
  if (view === "replays" && replay)
    throw redirect({
      to: "/$org/analytics/$project/$site/replays/$replay",
      params: { ...params, replay },
      search: kept,
    });
  const to =
    view === "vitals"
      ? "/$org/analytics/$project/$site/vitals"
      : view === "funnels"
        ? "/$org/analytics/$project/$site/funnels"
        : view === "replays"
          ? "/$org/analytics/$project/$site/replays"
          : "/$org/analytics/$project/$site";
  throw redirect({ to, params, search: kept });
}
