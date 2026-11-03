import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { currentOrg } from "../api/client";
import { useEventStream } from "../api/events";
import type { components } from "../api/schema";
import { applyAlertEvent } from "../features/alerts/data";
import { backupSettingsKey } from "../features/backups/settings";
import { cloudflareKey } from "../features/cloudflare/data";
import { registryKey } from "../features/registry/data";
import { diskKey } from "../features/servers/disk";
import { storageKey } from "../features/storage/data";
import { applyUpdateEvent } from "../features/update/data";
import { useOrg } from "../shell/location";
import { recordLiveEvent } from "./feed";
import { signedOut } from "./mutations";
import { followProjectRename, reconcileProjectNames } from "./projectRename";
import { findService, keys, queries } from "./queries";
import type {
  LiveEvent,
  LogLine,
  ProjectOverview,
  Release,
  ReleaseSummary,
  Server,
  ServerDetail,
  ServerStats,
  ServiceRef,
  ServiceStats,
  ServiceTraffic,
  ServiceView,
  StatsRange,
  TrafficPoint,
} from "./types";

// Live updates from the daemon over Server-Sent Events. Nothing polls:
// /api/v1/events patches or invalidates the query cache, and log views
// follow their own streams.

// Build output is followed on its own stream (useBuildLog), not the bus.
const busEvents = [
  "project_renamed",
  "release",
  "release_event",
  "service_status",
  "domain",
  "audit",
  "drift",
  "lagged",
  "usage",
  "traffic",
  "server",
  "alert",
  "update",
  "disk",
  "analytics",
] as const satisfies readonly Exclude<LiveEvent["type"], "build_log">[];

const refOf = (e: LiveEvent): ServiceRef | null =>
  e.project && e.environment && e.service
    ? { project: e.project, environment: e.environment, service: e.service }
    : null;

/** Replaces one service in a cached overview. */
export function patchService(
  queryClient: QueryClient,
  ref: ServiceRef,
  patch: (s: ServiceView) => ServiceView,
) {
  queryClient.setQueryData<ProjectOverview>(keys.overview(ref.project), (o) =>
    o
      ? {
          ...o,
          environments: o.environments.map((env) =>
            env.environment.name === ref.environment
              ? {
                  ...env,
                  services: env.services.map((s) =>
                    s.service.name === ref.service ? patch(s) : s,
                  ),
                }
              : env,
          ),
        }
      : o,
  );
}

/** Newest first, one entry per release number. */
function upsertRelease(list: Release[] | undefined, release: Release) {
  if (!list) return list;
  const rest = list.filter((r) => r.number !== release.number);
  return [release, ...rest].sort((a, b) => b.number - a.number);
}

/** A release as the overview summarizes it (core.summarize). */
const summarize = ({
  number,
  status,
  source,
  image_ref,
  image_id,
  commit,
  created_by,
  created_at,
  error,
  error_code,
}: Release): ReleaseSummary => ({
  number,
  status,
  source,
  image_ref,
  image_id,
  commit,
  created_by,
  created_at,
  error,
  error_code,
});

const inFlight = (status: Release["status"]) =>
  status !== "active" && status !== "failed" && status !== "superseded";

/**
 * Patches a release's new state into its service on the overview, as
 * core.Overview derives it: the active release, the one in flight and the
 * newest. A release that went active is published before the one it
 * superseded, so that one only clears its own role. A service the cached
 * overview doesn't have yet refetches the overview instead.
 */
function applyRelease(
  queryClient: QueryClient,
  ref: ServiceRef,
  release: Release,
) {
  const overview = queryClient.getQueryData<ProjectOverview>(
    keys.overview(ref.project),
  );
  if (!overview) return;
  if (!findService(overview, ref)) {
    queryClient.invalidateQueries({ queryKey: keys.overview(ref.project) });
    return;
  }
  const summary = summarize(release);
  const { number, status } = release;
  const role = (r: ReleaseSummary | undefined, has: boolean) =>
    has ? summary : r?.number === number ? undefined : r;
  patchService(queryClient, ref, (s) => {
    const active = role(s.active_release, status === "active");
    return {
      ...s,
      service: { ...s.service, active_release: active?.number },
      active_release: active,
      deploying: role(s.deploying, inFlight(status)),
      latest_release:
        (s.latest_release?.number ?? 0) > number ? s.latest_release : summary,
    };
  });
}

/**
 * Applies one event to the query cache. Frequent, small events (releases,
 * progress lines, status, domains) patch it in place; the rest invalidate
 * what they affect and let active queries refetch.
 */
export function applyLiveEvent(queryClient: QueryClient, event: LiveEvent) {
  const ref = refOf(event);
  switch (event.type) {
    case "release": {
      const release = event.release;
      if (!ref || !release) return;
      // Every cached length of the list (20, or 100 on the Releases tab).
      queryClient.setQueriesData<Release[]>(
        { queryKey: keys.releases(ref) },
        (list) => upsertRelease(list, release),
      );
      queryClient.setQueryData<Release>(
        keys.release(ref, release.number),
        (old) => old && { ...release, events: old.events },
      );
      applyRelease(queryClient, ref, release);
      // A queued push leaves the git view as its release starts building
      // (after core.gitDeployStart), and the next one may start once a
      // deploy ends. Refetches only while the view is shown.
      if (
        release.status === "building" ||
        release.status === "active" ||
        release.status === "failed"
      )
        queryClient.invalidateQueries({ queryKey: keys.git(ref) });
      return;
    }
    case "release_event": {
      const line = event.release_event;
      if (!ref || !line) return;
      queryClient.setQueryData<Release>(
        keys.release(ref, line.release),
        (old) => old && { ...old, events: [...(old.events ?? []), line] },
      );
      return;
    }
    case "service_status": {
      const status = event.service_status;
      if (!ref || !status) return;
      patchService(queryClient, ref, (s) => ({
        ...s,
        service: { ...s.service, status },
      }));
      return;
    }
    case "domain": {
      const domain = event.domain;
      if (!ref || !domain) return;
      const removed = event.message === "removed";
      patchService(queryClient, ref, (s) => {
        const rest = s.domains.filter((d) => d.hostname !== domain.hostname);
        return { ...s, domains: removed ? rest : [...rest, domain] };
      });
      return;
    }
    case "audit":
      invalidateForAudit(queryClient, event.audit);
      return;
    case "drift":
      queryClient.invalidateQueries({
        queryKey: event.project ? keys.overview(event.project) : keys.servers,
      });
      return;
    case "lagged":
      // Events were dropped: everything on screen may be stale.
      queryClient.invalidateQueries();
      return;
    case "usage":
      applyUsage(queryClient, event);
      return;
    case "traffic":
      applyTraffic(queryClient, event);
      return;
    case "server":
      // A server connected, disconnected, joined, changed or went away.
      applyServer(queryClient, event.server, event.message === "removed");
      return;
    case "alert":
      // Only admins get these; they patch the alert lists.
      if (event.alert) applyAlertEvent(queryClient, event.alert);
      return;
    case "update":
      // Only operators get these: an update moved on.
      if (event.update) applyUpdateEvent(queryClient, event.update);
      return;
    case "disk":
      // Only operators get these: a server's disk was read again.
      if (event.disk)
        queryClient.setQueryData(diskKey(event.disk.server), event.disk);
      return;
    case "analytics":
      // Only operators get these: the analytics store started or failed.
      queryClient.invalidateQueries({ queryKey: ["analytics", "status"] });
      return;
    case "project_renamed":
      return; // Navigation is handled by useLiveUpdates.
    case "build_log":
      return; // not subscribed, see busEvents
    default:
      // A new event type in the API fails the build until handled here.
      event.type satisfies never;
  }
}

/**
 * A server changed (connected, disconnected, joined, settings): patch it
 * into the server list and its page. A removed (or cancelled joining)
 * server leaves the list; its page, if open, keeps what it showed.
 */
function applyServer(
  queryClient: QueryClient,
  server: Server | undefined,
  removed: boolean,
) {
  if (!server) return;
  if (removed) {
    queryClient.setQueryData<Server[]>(keys.servers, (list) =>
      list?.filter((s) => s.id !== server.id),
    );
    return;
  }
  // A server added elsewhere isn't in the list yet.
  queryClient.setQueryData<Server[]>(keys.servers, (list) =>
    list?.some((s) => s.id === server.id)
      ? list.map((s) => (s.id === server.id ? { ...s, ...server } : s))
      : list && [...list, server],
  );
  // Connectors are read from Cloudflare for the server page only; an
  // event's tunnel keeps the ones the page has.
  queryClient.setQueryData<ServerDetail>(
    keys.server(server.name),
    (d) =>
      d && {
        ...d,
        server: {
          ...d.server,
          ...server,
          ...(server.tunnel &&
            d.server.tunnel?.connectors && {
              tunnel: {
                connectors: d.server.tunnel.connectors,
                ...server.tunnel,
              },
            }),
        },
      },
  );
}

/** The ranges with a point a minute, which usage events extend. */
const minuteRanges = [
  ["1h", 3_600_000],
  ["24h", 86_400_000],
] as const satisfies readonly (readonly [StatsRange, number])[];

/**
 * Appends a 1-minute point to cached history, dropping points that fell
 * out of the range. A point the history already has (it was refetched
 * after the tick) is left alone.
 */
function appendPoint<
  P extends { time: string },
  S extends { resolution_seconds: number; points: P[] },
>(stats: S | undefined, rangeMs: number, point: P): S | undefined {
  if (stats?.resolution_seconds !== 60) return stats;
  const t = Date.parse(point.time);
  const last = stats.points.at(-1);
  if (last && Date.parse(last.time) >= t) return stats;
  const from = t - rangeMs;
  const keep = stats.points.findIndex((p) => Date.parse(p.time) > from);
  const points = keep < 0 ? [] : stats.points.slice(keep);
  return { ...stats, points: [...points, point] };
}

/**
 * A minute's traffic of an environment's services (ADR 0033): a new point
 * on each open 24h view, added to its totals. Paths aren't in the event,
 * and hour points may already hold the minute, so those stay as loaded
 * until the view is fetched again.
 */
function applyTraffic(queryClient: QueryClient, event: LiveEvent) {
  const { project, environment, traffic } = event;
  if (!project || !environment || !traffic?.services) return;
  for (const { service, point } of traffic.services) {
    queryClient.setQueryData<ServiceTraffic>(
      keys.serviceTraffic({ project, environment, service }, "24h"),
      (t) => {
        const next = appendPoint(t, 86_400_000, point);
        return next === t || !next
          ? t
          : { ...next, totals: plus(next.totals, point) };
      },
    );
  }
}

/** Traffic counts with a minute's added. */
function plus(
  t: ServiceTraffic["totals"],
  p: TrafficPoint,
): ServiceTraffic["totals"] {
  const requests = t.requests + p.requests;
  return {
    ...t,
    requests,
    status_2xx: t.status_2xx + p.status_2xx,
    status_3xx: t.status_3xx + p.status_3xx,
    status_4xx: t.status_4xx + p.status_4xx,
    status_5xx: t.status_5xx + p.status_5xx,
    bytes: t.bytes + p.bytes,
    avg_duration_ms: requests
      ? (t.avg_duration_ms * t.requests + p.avg_duration_ms * p.requests) /
        requests
      : 0,
  };
}

/**
 * A minute's samples: the numbers on screen (overview, service pages,
 * server list and page) and, when the tick stored them, a new point on
 * open 1h and 24h charts. Only cached queries are touched.
 */
function applyUsage(queryClient: QueryClient, event: LiveEvent) {
  const usage = event.usage;
  if (!usage) return;
  const { server, host, point } = usage;
  if (host) {
    queryClient.setQueryData<Server[]>(keys.servers, (list) =>
      list?.map((s) => (s.name === server ? { ...s, usage: host } : s)),
    );
    queryClient.setQueryData<ServerDetail>(
      keys.server(server),
      (d) => d && { ...d, server: { ...d.server, usage: host } },
    );
    if (!point) return;
    for (const [range, ms] of minuteRanges) {
      queryClient.setQueryData<ServerStats>(
        keys.serverStats(server, range),
        (s) =>
          appendPoint(s, ms, {
            time: point,
            cpu_percent: host.cpu_percent,
            memory_used_bytes: host.memory_used_bytes,
            disk_used_bytes: host.disk_used_bytes,
            load1: host.load1,
          }),
      );
    }
    return;
  }
  const { project, environment } = event;
  if (!project || !environment || !usage.services?.length) return;
  const byName = new Map(usage.services.map((s) => [s.service, s.usage]));
  queryClient.setQueryData<ProjectOverview>(
    keys.overview(project),
    (o) =>
      o && {
        ...o,
        environments: o.environments.map((env) =>
          env.environment.name === environment
            ? {
                ...env,
                services: env.services.map((s) => {
                  const u = byName.get(s.service.name);
                  return u ? { ...s, service: { ...s.service, usage: u } } : s;
                }),
              }
            : env,
        ),
      },
  );
  queryClient.setQueryData<ServerDetail>(
    keys.server(server),
    (d) =>
      d && {
        ...d,
        services: d.services.map((s) => {
          const u =
            s.project === project && s.environment === environment
              ? byName.get(s.name)
              : undefined;
          return u ? { ...s, usage: u } : s;
        }),
      },
  );
  if (!point) return;
  for (const [service, u] of byName) {
    for (const [range, ms] of minuteRanges) {
      queryClient.setQueryData<ServiceStats>(
        keys.serviceStats({ project, environment, service }, range),
        (s) =>
          appendPoint(s, ms, {
            time: point,
            cpu_percent: u.cpu_percent,
            memory_bytes: u.memory_bytes,
            io_read_bytes_per_sec: u.io_read_bytes_per_sec,
            io_write_bytes_per_sec: u.io_write_bytes_per_sec,
          }),
      );
    }
  }
}

/**
 * Every mutation is audited, so audit events also say what someone else (or
 * the CLI) just changed.
 */
function invalidateForAudit(
  queryClient: QueryClient,
  audit: components["schemas"]["AuditEvent"] | undefined,
) {
  const invalidate = (queryKey: readonly unknown[]) =>
    queryClient.invalidateQueries({ queryKey });
  invalidate(["audit"]);
  if (!audit) return;
  if (audit.action === "project.rename") return; // Its typed event moves the cache.
  const [kind] = audit.action.split(".");
  if (kind === "project") invalidate(keys.projects);
  if (kind === "token") invalidate(keys.tokens);
  if (kind === "user") invalidate(keys.users);
  // Channels added, removed or alerts resolved (from the CLI too).
  if (kind === "alert") invalidate(["alerts"]);
  if (kind === "data") return invalidateForData(queryClient, audit);
  // The install's settings and the control plane's own backups.
  if (kind === "backup" && !audit.project) invalidate(backupSettingsKey);
  if (kind === "registry") invalidate(registryKey);
  // Storage connections, and which one the registry uses.
  if (kind === "storage" || kind === "registry") invalidate(storageKey);
  // Cloudflare connections, their zones and the tunnels they run.
  if (
    kind === "cloudflare" ||
    (kind === "server" && audit.action.startsWith("server.tunnel"))
  )
    invalidate(cloudflareKey);
  // Dock Analytics: sites, settings, the analytics server (ADR 0037). A
  // domain added or removed may carry a site along or leave it.
  if (kind === "analytics") invalidate(["analytics"]);
  if (kind === "domain") invalidate(["analytics", "sites"]);
  // Servers count what is placed on them.
  if (kind === "server" || kind === "environment") invalidate(keys.servers);
  if (!audit.project) return;
  // Release progress patches the overview (applyRelease), but starting a
  // release takes the service's staged changes and may set its image, and
  // a promotion may add services: the overview refetches, once per action.
  if (kind === "release") {
    invalidate(keys.overview(audit.project));
    return;
  }
  // Everything else in a project (variables, settings, changes, services,
  // environments) refetches.
  invalidate(
    audit.environment
      ? keys.env({ project: audit.project, environment: audit.environment })
      : keys.project(audit.project),
  );
  invalidate(keys.overview(audit.project));
}

// The parts of the database panel (keys.data(ref)[4]) each data.* action
// changes. Browsing and reveals change nothing on screen.
const dataParts: Partial<Record<string, readonly string[]>> = {
  "data.query": ["history"],
  "data.propose": ["changesets"],
  "data.reject": ["changesets"],
  "data.approve": ["changesets", "rows"],
  "data.commit": ["changesets", "rows"],
  "data.setup": ["info", "schema"],
};

/**
 * Refreshes only what a data.* audit event changed, never whole table
 * pages for a query: every SQL statement is audited.
 */
function invalidateForData(
  queryClient: QueryClient,
  audit: components["schemas"]["AuditEvent"],
) {
  if (audit.action === "data.policy") {
    // A policy or a token's override changed: anyone's access may differ.
    queryClient.invalidateQueries({ queryKey: keys.dataPolicy });
    queryClient.invalidateQueries({ queryKey: keys.tokens });
    queryClient.invalidateQueries({
      queryKey: ["data"],
      predicate: (q) => q.queryKey[4] === "info",
    });
    return;
  }
  const parts = dataParts[audit.action];
  if (!parts || !audit.project || !audit.environment) return;
  queryClient.invalidateQueries({
    queryKey: ["data", audit.project, audit.environment],
    predicate: (q) => parts.includes(String(q.queryKey[4])),
  });
}

/**
 * Keeps the cache live while the app shell is mounted. The stream carries
 * one organization's events, so it reconnects when the org changes.
 */
export function useLiveUpdates() {
  const queryClient = useQueryClient();
  const router = useRouter();
  const org = useOrg();
  const recover = async (invalidate: boolean) => {
    await reconcileProjectNames(queryClient, router, org);
    if (invalidate && org === currentOrg())
      await queryClient.invalidateQueries();
  };
  useEventStream<LiveEvent>(
    org ? `/api/v1/events?org=${encodeURIComponent(org)}` : "/api/v1/events",
    busEvents,
    (_, event) => {
      if (event.type === "project_renamed" && event.project_renamed) {
        void followProjectRename(
          queryClient,
          router,
          org,
          event.project_renamed,
        )
          .catch(() => recover(true))
          .catch(() => router.invalidate());
      } else if (event.type === "lagged") {
        void recover(true).catch(() => router.invalidate());
      } else {
        applyLiveEvent(queryClient, event);
      }
      recordLiveEvent(event);
    },
    {
      // Reconcile after subscribing, including the initial read/subscribe gap.
      onReady: (reconnected) => {
        void recover(reconnected).catch(() => router.invalidate());
      },
      // A refused stream usually means the session ended.
      onClose: async () => {
        const session = await queryClient.fetchQuery({
          ...queries.session(),
          staleTime: 0,
        });
        if (!session) {
          await signedOut(queryClient, router, router.state.location.href);
        }
      },
    },
  );
}

/** Lines kept per log view; older ones fall off. */
export const LOG_CAP = 2000;

export type LogStream = {
  /** Newest first. */
  lines: LogLine[];
  /** True while the stream is open. */
  live: boolean;
  error: string | null;
};

type StreamState = {
  url: string | null;
  lines: LogLine[];
  ended: boolean;
  error: string | null;
};

const fresh = (url: string | null): StreamState => ({
  url,
  lines: [],
  ended: false,
  error: null,
});

/**
 * Collects a finite line stream. Lines arrive in bursts (the backlog comes
 * all at once), so they are buffered and rendered once per frame.
 */
function useLineStream<T>(
  url: string | null,
  toLine: (data: T, n: number) => LogLine,
): LogStream {
  const [state, setState] = useState(() => fresh(url));
  const buffer = useRef<{ url: string | null; lines: LogLine[]; n: number }>({
    url,
    lines: [],
    n: 0,
  });
  const frame = useRef(0);

  useEffect(() => () => cancelAnimationFrame(frame.current), []);

  const flush = () => {
    frame.current = 0;
    const { url: from, lines } = buffer.current;
    buffer.current.lines = [];
    setState((s) => {
      const base = s.url === from ? s : fresh(from);
      return {
        ...base,
        lines: [...lines.reverse(), ...base.lines].slice(0, LOG_CAP),
      };
    });
  };

  useEventStream<T>(
    url,
    ["line"],
    (_, data) => {
      if (buffer.current.url !== url) buffer.current = { url, lines: [], n: 0 };
      buffer.current.lines.push(toLine(data, buffer.current.n++));
      if (!frame.current) frame.current = requestAnimationFrame(flush);
    },
    {
      finite: true,
      onClose: (error) => {
        // Show the backlog before saying the stream ended.
        cancelAnimationFrame(frame.current);
        flush();
        setState((s) => ({
          ...(s.url === url ? s : fresh(url)),
          ended: true,
          error,
        }));
      },
    },
  );

  const current = state.url === url ? state : fresh(url);
  return {
    lines: current.lines,
    live: url !== null && !current.ended,
    error: current.error,
  };
}

const serviceUrl = (ref: ServiceRef) =>
  `/api/v1/projects/${ref.project}/environments/${ref.environment}/services/${ref.service}`;

/**
 * How much backlog a log view starts with, like `dock logs`: the last
 * `tail` lines (0 for all), only those newer than `since` (10m, 1h, an
 * RFC 3339 time) when set.
 */
export type Backlog = { tail: number; since?: string };

/** Follows a release's container output: its backlog, then live. */
export const useServiceLogs = (
  ref: ServiceRef,
  release: number | null,
  { tail, since }: Backlog = { tail: 500 },
) =>
  useLineStream<components["schemas"]["LogLine"]>(
    release === null
      ? null
      : `${serviceUrl(ref)}/logs?follow=true&tail=${tail}&release=${release}${since ? `&since=${encodeURIComponent(since)}` : ""}`,
    (line, n) => ({
      key: `${release}:${n}`,
      time: line.time,
      stream: line.stream,
      text: line.text,
    }),
  );

/** A release's build (or image pull) output, live until the build ends. */
export const useBuildLog = (ref: ServiceRef, release: number | null) =>
  useLineStream<components["schemas"]["BuildLogLine"]>(
    release === null
      ? null
      : `${serviceUrl(ref)}/releases/${release}/build-log/stream`,
    (line) => ({
      key: `b${line.release}:${line.seq}`,
      time: null,
      stream: "build",
      text: line.text,
    }),
  );
