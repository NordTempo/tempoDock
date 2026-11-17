import { Link } from "@tanstack/react-router";
import { type ReactNode, useMemo, useState } from "react";
import type { Trace, TraceSpan } from "../../data/traces";
import { useOrg } from "../../shell/location";
import { ms } from "./format";

// A trace's waterfall (ADR 0075, mock B): its spans as a tree, each a bar
// at its offset in the trace, coloured by service, failures in red. A
// span opens its details: in place under it in a list, or beside the
// waterfall on the full trace page.

/** Spans a waterfall renders in place before "Open full trace". */
export const INLINE_SPANS = 200;

/** Bar colours by service, in the order services first appear. */
const serviceColors = [
  "bg-info/70",
  "bg-mem-platform/70",
  "bg-mem-kernel/70",
  "bg-disk-build/70",
  "bg-warn/60",
];

type Row = { span: TraceSpan; depth: number; last: boolean };

/** The spans in tree order: each after its parent, siblings by start. */
function treeOrder(spans: TraceSpan[]): Row[] {
  const ids = new Set(spans.map((s) => s.span));
  const children = new Map<string, TraceSpan[]>();
  const roots: TraceSpan[] = [];
  for (const s of spans) {
    if (s.parent && ids.has(s.parent)) {
      children.set(s.parent, [...(children.get(s.parent) ?? []), s]);
    } else {
      roots.push(s);
    }
  }
  const out: Row[] = [];
  // Iterative, so a 2,000-span chain can't overflow the stack.
  const stack: Row[] = roots
    .map((span, i) => ({ span, depth: 0, last: i === roots.length - 1 }))
    .reverse();
  while (stack.length) {
    const row = stack.pop() as Row;
    out.push(row);
    const kids = children.get(row.span.span) ?? [];
    for (let i = kids.length - 1; i >= 0; i--) {
      stack.push({
        span: kids[i] as TraceSpan,
        depth: row.depth + 1,
        last: i === kids.length - 1,
      });
    }
  }
  return out;
}

const failed = (s: TraceSpan) =>
  s.status === "error" || (s.http_status ?? 0) >= 500;

/** What a span is called in the waterfall. */
export const spanName = (s: TraceSpan) =>
  s.hidden
    ? "A service you can't see"
    : s.name || `${s.method ?? ""} ${s.route ?? ""}`.trim() || s.kind;

export function Waterfall({
  trace,
  limit,
  selected,
  onSelect,
  details,
}: {
  trace: Trace;
  /** At most this many rows; the rest are on the full trace page. */
  limit?: number;
  selected: string | undefined;
  onSelect: (span: string | undefined) => void;
  /** Rendered under the selected row: the in-place view's details. */
  details?: (span: TraceSpan) => ReactNode;
}) {
  const rows = useMemo(() => treeOrder(trace.spans), [trace.spans]);
  const colors = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of trace.spans) {
      if (s.service && !m.has(s.service)) {
        m.set(
          s.service,
          serviceColors[m.size % serviceColors.length] as string,
        );
      }
    }
    return m;
  }, [trace.spans]);
  const total = Math.max(trace.duration_ms, 0.001);
  const shown = limit ? rows.slice(0, limit) : rows;
  return (
    <div className="panel text-xs">
      <div className="grid grid-cols-[minmax(200px,38%)_1fr_72px] items-center gap-3 border-b border-line px-3 py-1.5 text-xs text-faint">
        <span>Span</span>
        <span className="relative h-3.5 tabular-nums">
          {[0, 0.25, 0.5, 0.75].map((f) => (
            <span key={f} className="absolute" style={{ left: `${f * 100}%` }}>
              {f === 0 ? "0" : ms(total * f)}
            </span>
          ))}
        </span>
        <span className="text-right">Duration</span>
      </div>
      <ul aria-label="Spans">
        {shown.map(({ span: s, depth }) => {
          const on = s.span === selected;
          const left = (s.offset_ms / total) * 100;
          const width = Math.max((s.duration_ms / total) * 100, 0.3);
          const bar = failed(s)
            ? "bg-fail"
            : s.kind === "edge" || s.hidden
              ? "bg-field"
              : (colors.get(s.service ?? "") ?? "bg-info/70");
          return (
            <li key={s.span}>
              <button
                type="button"
                aria-expanded={details ? on : undefined}
                aria-pressed={details ? undefined : on}
                onClick={() => onSelect(on ? undefined : s.span)}
                className={`grid w-full grid-cols-[minmax(200px,38%)_1fr_72px] items-center gap-3 border-b border-soft px-3 py-1 text-left hover:bg-hover ${on ? "bg-sel" : ""}`}
              >
                <span
                  className="flex min-w-0 items-baseline gap-1.5"
                  style={{ paddingLeft: `${Math.min(depth, 24) * 12}px` }}
                >
                  <span
                    className={`truncate ${failed(s) ? "text-fail" : s.hidden ? "text-faint" : ""}`}
                    title={spanName(s)}
                  >
                    {spanName(s)}
                  </span>
                  <span className="shrink-0 text-xs text-faint">
                    {s.kind === "edge"
                      ? "edge"
                      : s.service
                        ? `${s.service}${s.release ? ` r${s.release}` : ""}`
                        : ""}
                  </span>
                </span>
                <span className="relative h-2.5 rounded-xs bg-soft">
                  <span
                    className={`absolute inset-y-0 rounded-xs ${bar}`}
                    style={{
                      left: `${Math.min(left, 99.7)}%`,
                      width: `${Math.min(width, 100 - Math.min(left, 99.7))}%`,
                    }}
                  />
                </span>
                <span className="text-right tabular-nums text-sec">
                  {ms(s.duration_ms)}
                </span>
              </button>
              {on && details?.(s)}
            </li>
          );
        })}
      </ul>
      {shown.length < rows.length && (
        <p className="px-3 py-2 text-xs text-faint">
          {rows.length - shown.length} more spans on the full trace page.
        </p>
      )}
      {trace.truncated && (
        <p className="px-3 py-2 text-xs text-faint">
          The trace has more spans than Dock shows (2,000).
        </p>
      )}
    </div>
  );
}

/** A span's details: where it ran, its attributes and its exceptions. */
export function SpanDetails({
  span: s,
  trace,
  children,
}: {
  span: TraceSpan;
  /** The trace's ID, for its log lines. */
  trace: string;
  children?: ReactNode;
}) {
  const org = useOrg();
  const [all, setAll] = useState(false);
  if (s.hidden) {
    return (
      <p className="px-3 py-2 text-xs text-mut">
        This span is from a service your token can't read.
      </p>
    );
  }
  const attrs = Object.entries(s.attrs ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  const exceptions = (s.events ?? []).filter((e) => e.name === "exception");
  const where =
    s.project && s.environment && s.service
      ? { project: s.project, environment: s.environment, service: s.service }
      : undefined;
  return (
    <div className="flex flex-col gap-2 border-b border-soft bg-panel px-3 py-2.5 text-xs">
      <div className="text-xs text-mut">
        {[
          s.service,
          s.kind,
          `+${ms(s.offset_ms)}`,
          ms(s.duration_ms),
          s.http_status ? String(s.http_status) : "",
          s.status === "error" ? "error" : "",
        ]
          .filter(Boolean)
          .join(" · ")}
      </div>
      {exceptions.map((e) => {
        const a = e.attrs ?? {};
        return (
          <div
            key={`${e.at_ms}-${String(a["exception.type"])}`}
            className="font-mono text-xs"
          >
            <div className="text-fail">
              {String(a["exception.type"] ?? "exception")}
              {a["exception.message"]
                ? `: ${String(a["exception.message"])}`
                : ""}
            </div>
            {typeof a["exception.stacktrace"] === "string" && (
              <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap text-mut">
                {a["exception.stacktrace"]}
              </pre>
            )}
          </div>
        );
      })}
      {attrs.length > 0 && (
        <dl className="grid grid-cols-[minmax(120px,max-content)_1fr] gap-x-4 gap-y-0.5 font-mono text-xs">
          {(all ? attrs : attrs.slice(0, 12)).map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-mut">{k}</dt>
              <dd className="break-all">
                {typeof v === "string" ? v : JSON.stringify(v)}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {attrs.length > 12 && !all && (
        <button
          type="button"
          className="self-start text-xs text-mut underline"
          onClick={() => setAll(true)}
        >
          {attrs.length - 12} more attributes
        </button>
      )}
      <div className="flex flex-wrap gap-2">
        {where && (
          <Link
            className="btn sm no-underline"
            to="/$org/$project/$environment/$service/logs"
            params={{ org, ...where }}
            search={{ q: trace }}
          >
            Log lines with this trace ID
          </Link>
        )}
        {where && s.release ? (
          <Link
            className="btn sm no-underline"
            to="/$org/$project/$environment/$service/history/$number"
            params={{ org, ...where, number: String(s.release) }}
          >
            Release r{s.release}
          </Link>
        ) : null}
        {children}
      </div>
    </div>
  );
}
