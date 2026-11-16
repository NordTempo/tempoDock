import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useCanDeploy } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { plural } from "../../ui/format";
import { Act, Button, ErrorText, SectionHead } from "../../ui/kit";
import {
  type AppMetrics,
  type AppMetricsRange,
  appMetricsQuery,
  type ChartSpec,
  chartFlags,
  chartName,
  chartQuery,
  isAgg,
  usePinCharts,
} from "./data";
import { MetricChart } from "./MetricChart";

// The Metrics tab's app metrics (ADR 0075, mock C): the service's pinned
// charts of what its apps send over OTLP, Dock's charts of their runtime
// metrics until someone pins others, and Add chart: a metric, how it
// combines and the attribute that splits it, previewed before pinning.
// Pinning is a service setting (deploy access); the whole dashboard is
// saved in order.

const MAX_CHARTS = 24;

const specOf = (c: ChartSpec): ChartSpec => ({
  name: c.name,
  agg: c.agg || undefined,
  by: c.by || undefined,
});
const same = (a: ChartSpec, b: ChartSpec) =>
  a.name === b.name &&
  (a.agg ?? "") === (b.agg ?? "") &&
  (a.by ?? "") === (b.by ?? "");

export function AppMetricsPanel({
  serviceRef: ref,
  range,
}: {
  serviceRef: ServiceRef;
  range: AppMetricsRange;
}) {
  // Not suspense: a trace store that doesn't answer costs this section,
  // not the tab. The route prefetches it.
  const { data: m, error } = useQuery(appMetricsQuery(ref, range));
  const canDeploy = useCanDeploy(ref);
  const pin = usePinCharts(ref);
  const [adding, setAdding] = useState(false);
  // Unpin shows while editing: one button a chart is too much chrome.
  const [editing, setEditing] = useState(false);
  if (!m)
    return (
      <section aria-labelledby="app-metrics">
        <SectionHead id="app-metrics">App metrics</SectionHead>
        {error ? (
          <ErrorText error={error} />
        ) : (
          <p className="text-xs text-mut">Reading app metrics…</p>
        )}
      </section>
    );
  const from = Date.parse(m.from);
  const specs = m.charts.map(specOf);
  const save = (charts: ChartSpec[], said: string) =>
    pin.mutate({ charts, said });
  const at = `-p ${ref.project} -e ${ref.environment} -s ${ref.service}`;

  return (
    <section aria-labelledby="app-metrics">
      <SectionHead
        id="app-metrics"
        right={
          canDeploy &&
          !m.tracing_off &&
          m.metrics.length > 0 && (
            <>
              {m.charts.length > 0 && (
                <Button small onClick={() => setEditing(!editing)}>
                  {editing ? "Done" : "Edit"}
                </Button>
              )}
              {!adding && (
                <Act
                  small
                  onClick={() => setAdding(true)}
                  cli={`dock metrics pin METRIC ${at}`}
                >
                  Add chart
                </Act>
              )}
            </>
          )
        }
      >
        App metrics
      </SectionHead>
      {/* The heading keeps its own margin, as the tab's other sections. */}
      <div className="flex flex-col gap-3">
        <Why metrics={m} />
        {adding && (
          <AddChart
            serviceRef={ref}
            range={range}
            metrics={m}
            pinned={specs}
            pending={pin.isPending}
            at={at}
            onPin={(spec) => {
              save([...specs, spec], `Pinned ${chartName(spec)}`);
              setAdding(false);
            }}
            onCancel={() => setAdding(false)}
          />
        )}
        {pin.error && <ErrorText error={pin.error} />}
        {m.charts.length > 0 && (
          <div className="grid gap-x-8 gap-y-6 lg:grid-cols-2 2xl:grid-cols-3">
            {m.charts.map((c, i) => {
              const spec = specs[i] as ChartSpec;
              return (
                <MetricChart
                  key={chartName(spec)}
                  chart={c}
                  from={from}
                  stepSeconds={m.step_seconds}
                  title={
                    <>
                      {c.name}
                      {(c.agg || c.by) && (
                        <span className="font-normal text-mut">
                          {" "}
                          {[c.agg, c.by && `by ${c.by}`]
                            .filter(Boolean)
                            .join(" ")}
                        </span>
                      )}
                    </>
                  }
                  action={
                    canDeploy &&
                    editing && (
                      <Act
                        small
                        disabled={pin.isPending}
                        cli={`dock metrics unpin ${c.name} ${chartFlags(spec)} ${at}`.replace(
                          / +/g,
                          " ",
                        )}
                        onClick={() =>
                          save(
                            specs.filter((s) => !same(s, spec)),
                            `Unpinned ${chartName(spec)}`,
                          )
                        }
                      >
                        Unpin
                      </Act>
                    )
                  }
                />
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

/**
 * Why there is nothing to chart, or what the caps cut: tracing off, the
 * service's metrics going elsewhere, none sent yet, or a metric whose
 * new series were cut, with the attribute to drop.
 */
function Why({ metrics: m }: { metrics: AppMetrics }) {
  if (m.tracing_off)
    return (
      <p className="text-mut">
        App metrics come with tracing, which is off. An operator turns it on
        with <code>dock tracing on</code>; apps with an OpenTelemetry SDK then
        send their metrics with their next deploy.
      </p>
    );
  const cut = m.metrics.filter((x) => (x.cut ?? 0) > 0);
  return (
    <>
      {m.metrics_elsewhere && (
        <p className="text-mut">
          The service sets <code>{m.metrics_elsewhere}</code>, so its apps send
          their metrics there, not to Dock.
        </p>
      )}
      {!m.metrics_elsewhere && m.metrics.length === 0 && (
        <p className="text-mut">
          No app metrics in this range. Apps with an OpenTelemetry SDK send
          their runtime and own metrics with no setup.
        </p>
      )}
      {m.metrics.length > 0 && m.charts.length === 0 && (
        <p className="text-mut">
          No charts pinned. The service sends{" "}
          {plural(m.metrics.length, "metric")}: add a chart of one.
        </p>
      )}
      {cut.map((x) => (
        <p key={x.name} className="text-xs text-warn-text">
          {x.name}: {x.cut?.toLocaleString("en-US")} points of new series cut
          past the service's {m.max_series.toLocaleString("en-US")} series a
          day.
          {x.cut_by &&
            ` Its attribute ${x.cut_by} has the most values; drop it from the metric.`}
        </p>
      ))}
    </>
  );
}

/** Add chart: pick a metric, its aggregation and attribute; preview; pin. */
function AddChart({
  serviceRef: ref,
  range,
  metrics: m,
  pinned,
  pending,
  at,
  onPin,
  onCancel,
}: {
  serviceRef: ServiceRef;
  range: AppMetricsRange;
  metrics: AppMetrics;
  pinned: ChartSpec[];
  pending: boolean;
  at: string;
  onPin: (spec: ChartSpec) => void;
  onCancel: () => void;
}) {
  const names = m.metrics
    .filter((x) => x.series > 0)
    .map((x) => x.name)
    .sort();
  const [spec, setSpec] = useState<ChartSpec>({ name: names[0] ?? "" });
  const preview = useQuery({
    ...chartQuery(ref, spec, range),
    enabled: spec.name !== "",
  });
  const c = preview.data?.chart;
  const full = pinned.length >= MAX_CHARTS;
  const there = pinned.some((p) => same(p, spec));
  return (
    <div className="flex flex-col gap-3 border-y border-line py-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        <label className="flex items-center gap-2">
          <span className="text-mut">Metric</span>
          <select
            className="field py-1 font-mono text-xs"
            value={spec.name}
            onChange={(e) => setSpec({ name: e.target.value })}
          >
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2">
          <span className="text-mut">Shown as</span>
          <select
            className="field py-1 text-xs"
            value={spec.agg ?? ""}
            onChange={(e) =>
              setSpec({
                ...spec,
                agg: isAgg(e.target.value) ? e.target.value : undefined,
              })
            }
          >
            {(c?.aggs ?? []).map((a, i) => (
              <option key={a} value={i === 0 ? "" : a}>
                {a}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2">
          <span className="text-mut">Split by</span>
          <select
            className="field py-1 font-mono text-xs"
            value={spec.by ?? ""}
            onChange={(e) =>
              setSpec({ ...spec, by: e.target.value || undefined })
            }
          >
            <option value="">nothing</option>
            {(c?.attrs ?? []).map((a) => (
              <option key={a.key} value={a.key}>
                {a.key} ({a.values})
              </option>
            ))}
          </select>
        </label>
        <span className="ml-auto flex gap-2">
          <Act
            small
            primary
            disabled={!spec.name || pending || there || full}
            reason={
              there
                ? "Pinned already"
                : full
                  ? `${MAX_CHARTS} charts at most`
                  : undefined
            }
            cli={`dock metrics pin ${spec.name} ${chartFlags(spec)} ${at}`.replace(
              / +/g,
              " ",
            )}
            onClick={() => onPin(specOf(spec))}
          >
            Pin
          </Act>
          <Button small onClick={onCancel}>
            Cancel
          </Button>
        </span>
      </div>
      {preview.error && <ErrorText error={preview.error} />}
      {c && preview.data && (
        <div className="max-w-2xl">
          <MetricChart
            chart={c}
            from={Date.parse(preview.data.from)}
            stepSeconds={preview.data.step_seconds}
            title={chartName(spec)}
          />
        </div>
      )}
    </div>
  );
}
