import {
  keepPreviousData,
  type QueryKey,
  queryOptions,
  useQuery,
} from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, body, done, list, withOrg } from "../../api/client";
import { useEventStream } from "../../api/events";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import { usePageVisible } from "../stats/live";

// Dock Analytics (ADR 0037): sites, their dashboards and settings, the
// install's defaults and the analytics server. analytics.* audit events
// refetch the site's or the project's keys (data/live.ts relies on the
// ["analytics", view, project, site, ...] order); dashboards don't update
// by themselves, only the live view does, over its own stream.

type Schemas = components["schemas"];
export type Site = Schemas["Site"];
export type SiteStats = Schemas["SiteStats"];
export type SiteMarker = Schemas["SiteMarker"];
export type SiteSettings = Schemas["SiteSettings"];
export type SiteLive = Schemas["SiteLive"];
export type AnalyticsRow = Schemas["AnalyticsRow"];
export type AnalyticsStatus = Schemas["AnalyticsStatus"];

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

/** A site's dashboard for a range and filters. */
export const siteStatsQuery = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "stats",
      project,
      site,
      range,
      filters.map(filterParam),
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/stats", {
          params: {
            path: { project, site },
            query: { range, filter: filters.map(filterParam) },
          },
        }),
      ),
    // Changing the range or a filter keeps the old numbers up meanwhile.
    placeholderData: keepPreviousData,
  });

/**
 * One dimension's top values, for a panel tab the dashboard doesn't load
 * (exit pages, cities, ...). Loads when the tab is shown.
 */
export const useBreakdown = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
  dimension: string,
  enabled: boolean,
) =>
  useQuery({
    queryKey: [
      "analytics",
      "breakdown",
      project,
      site,
      range,
      filters.map(filterParam),
      dimension,
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/breakdown", {
          params: {
            path: { project, site },
            query: {
              dimension,
              range,
              limit: 10,
              filter: filters.map(filterParam),
            },
          },
        }),
      ).then((b) => b.rows),
    enabled,
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

/** An analytics mutation: refetches analytics, the audit log and `also`. */
const useAnalyticsAction = <Vars, Data>(
  mutationFn: (vars: Vars) => Promise<Data>,
  also: QueryKey[] = [],
) => useAction(mutationFn, () => [analyticsKey, ["audit"], ...also]);

export const useEnableHostname = () =>
  useAnalyticsAction(
    ({ hostname, site }: { hostname: string; site?: string }) =>
      body(
        api.PUT("/api/v1/analytics/hostnames/{hostname}", {
          params: { path: { hostname } },
          body: site ? { site } : {},
        }),
      ),
  );

export const useDisableHostname = () =>
  useAnalyticsAction((hostname: string) =>
    done(
      api.DELETE("/api/v1/analytics/hostnames/{hostname}", {
        params: { path: { hostname } },
      }),
    ),
  );

export const useUpdateSite = () =>
  useAnalyticsAction(
    ({
      project,
      site,
      change,
    }: {
      project: string;
      site: string;
      change: Schemas["UpdateSiteRequest"];
    }) =>
      body(
        api.PATCH("/api/v1/projects/{project}/sites/{site}", {
          params: { path: { project, site } },
          body: change,
        }),
      ),
  );

export const useSetDefaults = () =>
  useAnalyticsAction((change: Schemas["UpdateSiteRequest"]) =>
    body(api.PATCH("/api/v1/analytics/defaults", { body: change })),
  );

// Servers show which one runs the store.
export const useSetAnalyticsServer = () =>
  useAnalyticsAction(
    (move: { server: string; start_empty?: boolean }) =>
      body(api.PUT("/api/v1/analytics/server", { body: move })),
    [keys.servers],
  );

/**
 * A site's live view while the browser tab is visible: the daemon watches
 * the site only while a view is open (ADR 0037). null until the first view
 * arrives; lost when the stream failed.
 */
export function useSiteLive(project: string, site: string) {
  const visible = usePageVisible();
  const [view, setView] = useState<{ site: string; live: SiteLive } | null>(
    null,
  );
  // A failed stream stays closed for its site until the tab is hidden and
  // shown again; another site gets its own try.
  const [failed, setFailed] = useState<{ site: string; error: string } | null>(
    null,
  );
  const lost = failed?.site === site ? failed.error : null;
  useEffect(() => {
    if (!visible) setFailed(null);
  }, [visible]);
  useEventStream<SiteLive>(
    visible && !lost
      ? `/api/v1/projects/${encodeURIComponent(project)}/sites/${encodeURIComponent(site)}/live`
      : null,
    ["live"],
    (_, live) => setView({ site, live }),
    {
      onClose: (error) =>
        setFailed({ site, error: error ?? "the live view ended" }),
    },
  );
  return {
    live: view?.site === site ? view.live : null,
    lost,
    paused: !visible,
  };
}

export type ReplayDetail = Schemas["ReplayDetail"];

/** One session replay: its page loads and timeline. */
export const replayQuery = (project: string, site: string, replay: string) =>
  queryOptions({
    queryKey: ["analytics", "replay", project, site, replay],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/replays/{replay}", {
          params: { path: { project, site, replay } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
  });

/** An rrweb event as the recorder sent it. */
export type RecordedEvent = { type: number; timestamp: number; data: unknown };

/**
 * A replay's recording: every page load's rrweb events, their times moved
 * to the server's clock (each page load's own skew), oldest first. The
 * API streams JSON lines, a {"segment": id} line before each page load's
 * chunks; fetching it is audited, once per call.
 */
export async function loadRecording(
  project: string,
  site: string,
  replay: ReplayDetail,
  signal: AbortSignal,
): Promise<RecordedEvent[]> {
  const path = `/api/v1/projects/${encodeURIComponent(project)}/sites/${encodeURIComponent(site)}/replays/${encodeURIComponent(replay.id)}/events`;
  const res = await fetch(withOrg(path), { credentials: "include", signal });
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  const skews = new Map(
    (replay.segment_list ?? []).map((s) => [s.id, s.skew_ms]),
  );
  const events: RecordedEvent[] = [];
  let skew = 0;
  for (const line of (await res.text()).split("\n")) {
    if (!line) continue;
    const v: unknown = JSON.parse(line);
    if (Array.isArray(v)) {
      for (const e of v as RecordedEvent[])
        events.push({ ...e, timestamp: e.timestamp + skew });
    } else if (v && typeof v === "object" && "segment" in v) {
      skew = skews.get(String(v.segment)) ?? 0;
    }
  }
  return events.sort((a, b) => a.timestamp - b.timestamp);
}

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

/** A change a view makes to the shared search. */
export type AnalyticsChange = {
  site?: string;
  range?: StatsRange;
  filters?: Filter[];
};

/** s with change applied; defaults (30d, no filters) leave the URL. */
export function applyChange<S extends AnalyticsSearch>(
  s: S,
  change: AnalyticsChange,
): S {
  const next = { ...s };
  if ("site" in change) next.site = change.site;
  if (change.range)
    next.range = change.range === "30d" ? undefined : change.range;
  if (change.filters)
    next.f = change.filters.length
      ? change.filters.map(filterParam)
      : undefined;
  return next;
}

/** The shared search of a site, range and filters, for links. */
export const searchOf = (
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
): AnalyticsSearch => ({
  site,
  ...(range !== "30d" && { range }),
  ...(filters.length > 0 && { f: filters.map(filterParam) }),
});

export type SiteErrors = Schemas["SiteErrors"];
export type ErrorGroup = Schemas["ErrorGroup"];
export type SiteError = Schemas["SiteError"];
export type SiteVitals = Schemas["SiteVitals"];
export type VitalsRow = Schemas["VitalsRow"];
export type VitalStat = Schemas["VitalStat"];

/** What errors and vitals filter by: what they record. */
export const errorDimensions = [
  "page",
  "hostname",
  "country",
  "browser",
  "browser_version",
  "os",
  "device",
  "service",
  "release",
] as const;

/** A site's error groups for a range and filters, with the totals. */
export const siteErrorsQuery = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "errors",
      project,
      site,
      range,
      filters.map(filterParam),
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/errors", {
          params: {
            path: { project, site },
            query: { range, filter: filters.map(filterParam), limit: 100 },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });

/** One error group: its stack, where it happened, latest occurrences. */
export const siteErrorQuery = (
  project: string,
  site: string,
  group: string,
  range: StatsRange,
  filters: readonly Filter[],
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "error",
      project,
      site,
      group,
      range,
      filters.map(filterParam),
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/errors/{group}", {
          params: {
            path: { project, site, group },
            query: { range, filter: filters.map(filterParam) },
          },
        }),
      ),
  });

/** The vitals a view charts. */
export const vitals = ["lcp", "inp", "cls", "fcp", "ttfb"] as const;
export type Vital = (typeof vitals)[number];

/** A site's Web Vitals with one metric over time and its breakdowns. */
export const siteVitalsQuery = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
  metric: Vital,
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "vitals",
      project,
      site,
      range,
      filters.map(filterParam),
      metric,
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/vitals", {
          params: {
            path: { project, site },
            query: {
              range,
              filter: filters.map(filterParam),
              dimension: ["page", "device", "browser", "country"],
              metric,
              limit: 20,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });

export type SiteReplays = Schemas["SiteReplays"];
export type Replay = Schemas["Replay"];

/** What replays filter by. */
export const replayDimensions = [
  "page",
  "country",
  "browser",
  "os",
  "device",
  "release",
] as const;

/** A site's replays that began in a range, newest first. */
export const siteReplaysQuery = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
  errorsOnly: boolean,
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "replays",
      project,
      site,
      range,
      filters.map(filterParam),
      errorsOnly,
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/replays", {
          params: {
            path: { project, site },
            query: {
              range,
              filter: filters.map(filterParam),
              errors: errorsOnly,
              limit: 200,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });

export type SiteGoals = Schemas["SiteGoals"];
export type Goal = Schemas["Goal"];
export type GoalStep = Schemas["GoalStep"];
export type Funnel = Schemas["Funnel"];
export type SiteFunnel = Schemas["SiteFunnel"];

/** A site's goals with their conversions for a range and filters. */
export const siteGoalsQuery = (
  project: string,
  site: string,
  range: StatsRange,
  filters: readonly Filter[],
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "goals",
      project,
      site,
      range,
      filters.map(filterParam),
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/goals", {
          params: {
            path: { project, site },
            query: { range, filter: filters.map(filterParam) },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });

export type PublicAddress = Schemas["PublicAddress"];

/** Where a site's public dashboard is and whether each address works,
 * checked by the server when asked. */
export const sitePublicQuery = (project: string, site: string) =>
  queryOptions({
    queryKey: ["analytics", "public", project, site],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/public", {
          params: { path: { project, site } },
        }),
      ),
    staleTime: 0,
  });

/** A site's funnels. */
export const siteFunnelsQuery = (project: string, site: string) =>
  queryOptions({
    queryKey: ["analytics", "funnels", project, site],
    queryFn: () =>
      list(
        api.GET("/api/v1/projects/{project}/sites/{site}/funnels", {
          params: { path: { project, site } },
        }),
      ),
  });

/** One funnel over a range and filters. */
export const siteFunnelQuery = (
  project: string,
  site: string,
  funnel: string,
  range: StatsRange,
  filters: readonly Filter[],
) =>
  queryOptions({
    queryKey: [
      "analytics",
      "funnel",
      project,
      site,
      funnel,
      range,
      filters.map(filterParam),
    ],
    queryFn: () =>
      body(
        api.GET("/api/v1/projects/{project}/sites/{site}/funnels/{funnel}", {
          params: {
            path: { project, site, funnel },
            query: { range, filter: filters.map(filterParam) },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });

export const useAddGoal = () =>
  useAnalyticsAction(
    ({
      project,
      site,
      goal,
    }: {
      project: string;
      site: string;
      goal: Schemas["CreateGoalRequest"];
    }) =>
      body(
        api.POST("/api/v1/projects/{project}/sites/{site}/goals", {
          params: { path: { project, site } },
          body: goal,
        }),
      ),
  );

export const useRemoveGoal = () =>
  useAnalyticsAction(
    ({
      project,
      site,
      goal,
    }: {
      project: string;
      site: string;
      goal: string;
    }) =>
      done(
        api.DELETE("/api/v1/projects/{project}/sites/{site}/goals/{goal}", {
          params: { path: { project, site, goal } },
        }),
      ),
  );

export const useAddFunnel = () =>
  useAnalyticsAction(
    ({
      project,
      site,
      funnel,
    }: {
      project: string;
      site: string;
      funnel: Schemas["CreateFunnelRequest"];
    }) =>
      body(
        api.POST("/api/v1/projects/{project}/sites/{site}/funnels", {
          params: { path: { project, site } },
          body: funnel,
        }),
      ),
  );

export const useRemoveFunnel = () =>
  useAnalyticsAction(
    ({
      project,
      site,
      funnel,
    }: {
      project: string;
      site: string;
      funnel: string;
    }) =>
      done(
        api.DELETE("/api/v1/projects/{project}/sites/{site}/funnels/{funnel}", {
          params: { path: { project, site, funnel } },
        }),
      ),
  );
