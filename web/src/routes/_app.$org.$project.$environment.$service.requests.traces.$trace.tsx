import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { servicePath } from "../data/queries";
import { traceQuery } from "../data/traces";
import { ms } from "../features/traces/format";
import { SpanDetails, Waterfall } from "../features/traces/Waterfall";
import { useOrg } from "../shell/location";
import { clockSec } from "../ui/format";
import { CopyButton } from "../ui/kit";

// A trace's own page (ADR 0075, mock B's "Open full trace"): every span
// with room for wide attributes, the selected span's details beside the
// waterfall. at is a time in the trace, which every link has.
// /shop/production/web/requests/traces/4f2a9c1e…?at=2026-11-11T14:02:11Z
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests/traces/$trace",
)({
  validateSearch: (s: Record<string, unknown>): { at?: string } => ({
    at: typeof s.at === "string" ? s.at : undefined,
  }),
  loaderDeps: ({ search }) => ({ at: search.at }),
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(
      traceQuery(servicePath(params), params.trace, deps.at),
    ),
  component: TracePage,
});

function TracePage() {
  const org = useOrg();
  const params = Route.useParams();
  const ref = servicePath(params);
  const { at } = Route.useSearch();
  const { data: trace } = useSuspenseQuery(traceQuery(ref, params.trace, at));
  const root = trace.spans.find((s) => !s.parent) ?? trace.spans[0];
  // The root cause first: the span with an exception, else the latest
  // failing one, which is the deepest.
  const failedSpan =
    trace.spans.find((s) => s.events?.some((e) => e.name === "exception")) ??
    trace.spans.findLast(
      (s) => s.status === "error" || (s.http_status ?? 0) >= 500,
    );
  const [span, setSpan] = useState(failedSpan?.span);
  const selected = trace.spans.find((s) => s.span === span);
  const services = new Set(
    trace.spans.flatMap((s) => (s.service ? [s.service] : [])),
  );
  return (
    <section aria-label="Trace" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-[13px] text-mut">
        <Link
          to="/$org/$project/$environment/$service/requests/traces"
          params={{ org, ...ref }}
          className="text-xs"
        >
          Traces
        </Link>
        <span className="font-mono text-fg">{trace.trace.slice(0, 16)}</span>
        {root && <span className="text-fg">{root.name}</span>}
        <span>started {clockSec(trace.start)}</span>
        <span>{ms(trace.duration_ms)}</span>
        <span>
          {trace.spans.length} spans in {services.size}{" "}
          {services.size === 1 ? "service" : "services"}
        </span>
        <CopyButton text={trace.trace} label="Copy ID" />
      </div>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(280px,34%)]">
        <Waterfall trace={trace} selected={span} onSelect={setSpan} />
        <div className="lg:sticky lg:top-4 lg:self-start">
          {selected ? (
            <div className="panel">
              <div
                className={`px-3 pt-2.5 font-semibold ${selected.status === "error" ? "text-fail" : ""}`}
              >
                {selected.name}
              </div>
              <SpanDetails span={selected} />
            </div>
          ) : (
            <p className="text-xs text-mut">
              Pick a span to see its attributes.
            </p>
          )}
        </div>
      </div>
    </section>
  );
}
