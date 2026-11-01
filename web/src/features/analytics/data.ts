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
// refetch everything under analyticsKey (data/live.ts); dashboards don't
// update by themselves, only the live view does, over its own stream.

type Schemas = components["schemas"];
export type Site = Schemas["Site"];
export type SiteStats = Schemas["SiteStats"];
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
