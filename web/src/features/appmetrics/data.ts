import { queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components, operations } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { servicePath } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// A service's app metrics (ADR 0075, phase 6): its pinned charts and
// what its apps send over OTLP, read when the Metrics tab opens. Points
// arrive a minute apart, so nothing here is live: a dashboard is fresh
// for half a minute and reloads when the tab regains focus.

type Schemas = components["schemas"];
export type AppMetricChart = Omit<
  Schemas["AppMetricChart"],
  "aggs" | "lines" | "attrs"
> & {
  aggs: string[];
  lines: AppMetricLine[];
  attrs: Schemas["AppMetricAttr"][];
};
export type AppMetricLine = Omit<Schemas["AppMetricLine"], "values"> & {
  values: (number | null)[];
};
export type AppMetric = Schemas["AppMetric"];
export type ChartSpec = Schemas["MetricChartSpec"];
/** The API's dashboard, its lists never null. */
export type AppMetrics = Omit<Schemas["AppMetrics"], "charts" | "metrics"> & {
  charts: AppMetricChart[];
  metrics: AppMetric[];
};
export type AppMetricsRange = NonNullable<
  NonNullable<operations["get-app-metrics"]["parameters"]["query"]>["range"]
>;
export type ChartAgg = NonNullable<ChartSpec["agg"]>;

/** Every aggregation, to read one back from a select. */
const aggs = [
  "sum",
  "max",
  "min",
  "avg",
  "rate",
  "p50",
  "p95",
  "p99",
] as const satisfies readonly ChartAgg[];
export const isAgg = (s: string): s is ChartAgg =>
  (aggs as readonly string[]).includes(s);

const path =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/app-metrics" as const;

export const appMetricsKey = (s: ServiceRef) =>
  [
    "project",
    s.project,
    "env",
    s.environment,
    "service",
    s.service,
    "app-metrics",
  ] as const;

/** Nulls from the API as empty lists, so charts never check. */
const chart = (c: Schemas["AppMetricChart"]): AppMetricChart => ({
  ...c,
  aggs: c.aggs ?? [],
  attrs: c.attrs ?? [],
  lines: (c.lines ?? []).map((l) => ({ ...l, values: l.values ?? [] })),
});

export const appMetricsQuery = (ref: ServiceRef, range: AppMetricsRange) =>
  queryOptions({
    queryKey: [...appMetricsKey(ref), range],
    queryFn: async (): Promise<AppMetrics> => {
      const m = await body(
        api.GET(path, {
          params: { path: servicePath(ref), query: { range } },
        }),
      );
      return {
        ...m,
        charts: (m.charts ?? []).map(chart),
        metrics: m.metrics ?? [],
      };
    },
    staleTime: 30_000,
  });

/** One metric as a chart, pinned or not: the Add chart preview. */
export const chartQuery = (
  ref: ServiceRef,
  spec: ChartSpec,
  range: AppMetricsRange,
) =>
  queryOptions({
    queryKey: [...appMetricsKey(ref), "chart", spec, range],
    queryFn: async () => {
      const r = await body(
        api.GET(`${path}/chart`, {
          params: {
            path: servicePath(ref),
            query: {
              name: spec.name,
              agg: spec.agg || undefined,
              by: spec.by || undefined,
              range,
            },
          },
        }),
      );
      return { ...r, chart: chart(r.chart) };
    },
    staleTime: 30_000,
  });

/** Pins the service's charts, the whole dashboard in order. */
export const usePinCharts = (ref: ServiceRef) =>
  useAction(
    ({ charts }: { charts: ChartSpec[]; said: string }) =>
      body(
        api.PUT(`${path}/charts`, {
          params: { path: servicePath(ref) },
          body: { charts },
        }),
      ),
    () => [appMetricsKey(ref)],
    ({ said }) => said,
  );

/** A chart's name: "queue.depth max by queue". */
export const chartName = (s: ChartSpec) =>
  [s.name, s.agg, s.by && `by ${s.by}`].filter(Boolean).join(" ");

/** The `dock metrics` flags of a chart, after its name. */
export const chartFlags = (s: ChartSpec) =>
  [s.agg && `--agg ${s.agg}`, s.by && `--by ${s.by}`].filter(Boolean).join(" ");
