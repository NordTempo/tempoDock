import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import {
  type TraceFilter,
  type TraceSummary,
  traceQuery,
  traceRanges,
  tracesQuery,
} from "../../data/traces";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { clockSec } from "../../ui/format";
import { ErrorText } from "../../ui/kit";
import { RangeSwitch } from "../stats/parts";
import { ms } from "./format";
import { INLINE_SPANS, SpanDetails, Waterfall } from "./Waterfall";

// Requests' Traces view (ADR 0075, mock B): the service's traces, each
// its entry span (Caddy's edge span, or the app's), newest or slowest
// first; a row opens its waterfall in place, and a span its details, the
// way Errors rows open. "Open full trace" is the trace's own page.

const kept = {
  sampled: "text-mut",
  slow: "text-warn-text",
  error: "text-fail",
  forced: "text-mut",
} as const;

export function TracesView({
  serviceRef,
  filter,
  onFilter,
}: {
  serviceRef: ServiceRef;
  filter: TraceFilter;
  onFilter: (next: TraceFilter) => void;
}) {
  const { data: list } = useSuspenseQuery(tracesQuery(serviceRef, filter));
  const [open, setOpen] = useState<string>();
  const longest = Math.max(1, ...list.traces.map((t) => t.duration_ms));
  return (
    <section aria-label="Traces" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
        <RangeSwitch
          value={filter.range ?? "24h"}
          options={traceRanges}
          onChange={(range) => onFilter({ ...filter, range })}
        />
        <fieldset className="seg" aria-label="Order">
          <button
            type="button"
            aria-pressed={!filter.slowest}
            onClick={() => onFilter({ ...filter, slowest: false })}
          >
            Newest
          </button>
          <button
            type="button"
            aria-pressed={!!filter.slowest}
            onClick={() => onFilter({ ...filter, slowest: true })}
          >
            Slowest
          </button>
        </fieldset>
        <label className="flex items-center gap-1.5 text-sec">
          <input
            type="checkbox"
            className="accent-fg"
            checked={!!filter.errors}
            onChange={(e) => onFilter({ ...filter, errors: e.target.checked })}
          />
          Failed only
        </label>
        {filter.route && (
          <button
            type="button"
            className="btn sm"
            onClick={() => onFilter({ ...filter, route: undefined })}
          >
            {filter.route} ×
          </button>
        )}
      </div>
      {list.traces.length === 0 ? (
        <p className="text-mut">
          No traces in this range. Requests to the service's domains are traced
          while tracing is on, and apps with an OpenTelemetry SDK add their own
          spans.
        </p>
      ) : (
        <div className="panel overflow-x-auto">
          <table className="tabular-nums">
            <thead>
              <tr>
                <th>At</th>
                <th>Trace</th>
                <th>Request</th>
                <th>Duration</th>
                <th className="text-right">Status</th>
                <th>Release</th>
                <th>Kept</th>
              </tr>
            </thead>
            <tbody>
              {list.traces.map((t) => (
                <TraceRow
                  key={t.span}
                  trace={t}
                  serviceRef={serviceRef}
                  open={open === t.span}
                  longest={longest}
                  onOpen={() => setOpen(open === t.span ? undefined : t.span)}
                  onRoute={(route) => onFilter({ ...filter, route })}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {list.more && (
        <p className="text-xs text-faint">
          More traces match; narrow the range or the filters.
        </p>
      )}
    </section>
  );
}

function TraceRow({
  trace: t,
  serviceRef,
  open,
  longest,
  onOpen,
  onRoute,
}: {
  trace: TraceSummary;
  serviceRef: ServiceRef;
  open: boolean;
  longest: number;
  onOpen: () => void;
  onRoute: (route: string) => void;
}) {
  const status = t.http_status
    ? String(t.http_status)
    : t.error
      ? "error"
      : "ok";
  return (
    <>
      <tr
        className={`cursor-pointer hover:bg-hover ${open ? "bg-sel" : ""}`}
        onClick={onOpen}
      >
        <td className="text-mut">{clockSec(t.start)}</td>
        <td>
          <button
            type="button"
            aria-expanded={open}
            className="font-mono text-mut"
            onClick={(e) => (e.stopPropagation(), onOpen())}
          >
            {t.trace.slice(0, 8)}
          </button>
        </td>
        <td className="max-w-sm truncate font-mono">
          {t.route ? (
            <button
              type="button"
              title={`Only ${t.route}`}
              className="hover:underline"
              onClick={(e) => (e.stopPropagation(), onRoute(t.route ?? ""))}
            >
              {`${t.method ?? ""} ${t.route}`.trim()}
            </button>
          ) : (
            t.name
          )}
        </td>
        <td className="whitespace-nowrap">
          <span
            className={`mr-2 inline-block h-1.5 rounded-xs align-middle ${t.error ? "bg-fail" : "bg-info/55"}`}
            style={{
              width: `${Math.max(2, (t.duration_ms / longest) * 80)}px`,
            }}
          />
          {ms(t.duration_ms)}
        </td>
        <td className={`text-right ${t.error ? "text-fail" : "text-sec"}`}>
          {status}
        </td>
        <td className="text-mut">{t.release ? `r${t.release}` : ""}</td>
        <td className={kept[t.kept]}>{t.kept}</td>
      </tr>
      {open && (
        <tr>
          <td colSpan={7} className="p-0">
            <OpenTrace serviceRef={serviceRef} summary={t} />
          </td>
        </tr>
      )}
    </>
  );
}

/** A list row's trace, read when it opens. */
function OpenTrace({
  serviceRef,
  summary,
}: {
  serviceRef: ServiceRef;
  summary: TraceSummary;
}) {
  const org = useOrg();
  const { data, error, isPending } = useQuery(
    traceQuery(serviceRef, summary.trace, summary.start),
  );
  const [span, setSpan] = useState<string>();
  if (error)
    return (
      <div className="px-3 py-2">
        <ErrorText error={error} />
      </div>
    );
  if (isPending)
    return <p className="px-3 py-2 text-xs text-mut">Reading the trace…</p>;
  const full = (
    <Link
      className="btn sm no-underline"
      to="/$org/$project/$environment/$service/requests/traces/$trace"
      params={{ org, ...serviceRef, trace: summary.trace }}
      search={{ at: summary.start }}
    >
      Open full trace
    </Link>
  );
  return (
    <div className="flex flex-col gap-2 py-2 pl-3">
      <Waterfall
        trace={data}
        limit={INLINE_SPANS}
        selected={span}
        onSelect={setSpan}
        details={(s) => <SpanDetails span={s} />}
      />
      <div className="flex gap-2">{full}</div>
    </div>
  );
}
