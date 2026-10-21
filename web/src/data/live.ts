import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useEventStream } from "../api/events";
import type { components } from "../api/schema";
import { signedOut } from "./mutations";
import { keys, queries } from "./queries";
import type {
  LiveEvent,
  LogLine,
  ProjectOverview,
  Release,
  ServiceRef,
  ServiceView,
} from "./types";

// Live updates from the daemon over Server-Sent Events. Nothing polls:
// /api/v1/events patches or invalidates the query cache, and log views
// follow their own streams.

// Build output is followed on its own stream (useBuildLog), not the bus.
const busEvents = [
  "release",
  "release_event",
  "service_status",
  "domain",
  "audit",
  "drift",
  "lagged",
] as const satisfies readonly Exclude<LiveEvent["type"], "build_log">[];

const refOf = (e: LiveEvent): ServiceRef | null =>
  e.project && e.environment && e.service
    ? { project: e.project, environment: e.environment, service: e.service }
    : null;

/** Replaces one service in a cached overview. */
function patchService(
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

/**
 * Applies one event to the query cache. Frequent, small events (progress
 * lines, status, domains) patch it in place; the rest invalidate what they
 * affect and let active queries refetch.
 */
export function applyLiveEvent(queryClient: QueryClient, event: LiveEvent) {
  const ref = refOf(event);
  switch (event.type) {
    case "release": {
      const release = event.release;
      if (!ref || !release) return;
      queryClient.setQueryData<Release[]>(keys.releases(ref), (list) =>
        upsertRelease(list, release),
      );
      queryClient.setQueryData<Release>(
        keys.release(ref, release.number),
        (old) => old && { ...release, events: old.events },
      );
      // Which release is active, deploying or latest lives in the overview.
      queryClient.invalidateQueries({ queryKey: keys.overview(ref.project) });
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
    case "build_log":
      return; // not subscribed, see busEvents
    default:
      // A new event type in the API fails the build until handled here.
      event.type satisfies never;
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
  const [kind] = audit.action.split(".");
  if (kind === "project") invalidate(keys.projects);
  if (kind === "token") invalidate(keys.tokens);
  if (kind === "user") invalidate(keys.users);
  if (!audit.project) return;
  // Release progress has its own events; everything else in a project
  // (variables, settings, changes, services, environments) refetches.
  if (kind === "release") return;
  invalidate(
    audit.environment
      ? keys.env({ project: audit.project, environment: audit.environment })
      : keys.project(audit.project),
  );
  invalidate(keys.overview(audit.project));
}

/** Keeps the cache live while the app shell is mounted. */
export function useLiveUpdates() {
  const queryClient = useQueryClient();
  const router = useRouter();
  useEventStream<LiveEvent>(
    "/api/v1/events",
    busEvents,
    (_, event) => applyLiveEvent(queryClient, event),
    {
      // Events may have been missed while disconnected.
      onReconnect: () => queryClient.invalidateQueries(),
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

/** Follows a release's container output (tail 500, then live). */
export const useServiceLogs = (ref: ServiceRef, release: number | null) =>
  useLineStream<components["schemas"]["LogLine"]>(
    release === null
      ? null
      : `${serviceUrl(ref)}/logs?follow=true&tail=500&release=${release}`,
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
