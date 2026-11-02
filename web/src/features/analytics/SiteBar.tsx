import { useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import type { ServiceRef } from "../../data/types";
import { Button } from "../../ui/kit";
import { RangeSwitch } from "../stats/parts";
import {
  type Filter,
  type Site,
  type SiteStats,
  type StatsRange,
  searchOf,
  siteStatsQuery,
  statsRanges,
} from "./data";
import { shown } from "./parts";

// The bar above every analytics view (ADR 0037): the site, its settings,
// the range and filters they share, and the views as sub-tabs (mock A).

const SiteSettingsDialog = lazy(() => import("./SiteSettingsDialog"));

/** The views of a site, as sub-tabs: their route's last segment. */
export const views = [
  ["Overview", "/$org/$project/$environment/$service/analytics"],
  ["Errors", "/$org/$project/$environment/$service/analytics/errors"],
  ["Vitals", "/$org/$project/$environment/$service/analytics/vitals"],
  ["Replays", "/$org/$project/$environment/$service/analytics/replays"],
  ["Funnels", "/$org/$project/$environment/$service/analytics/funnels"],
] as const;
export type View = (typeof views)[number][1];

export function SiteBar({
  serviceRef: ref,
  sites,
  site,
  view,
  range,
  filters,
  labels,
  onSite,
  onRenamed,
  onRange,
  onFilters,
}: {
  serviceRef: ServiceRef;
  /** The sites to choose from: those the service serves. */
  sites: Site[];
  site: Site;
  view: View;
  range: StatsRange;
  filters: Filter[];
  /** Labels of filtered values (services, releases) from rows clicked. */
  labels: Record<string, string>;
  onSite: (name: string) => void;
  /** The site was renamed: keep showing it, filters and all. */
  onRenamed: (name: string) => void;
  onRange: (range: StatsRange) => void;
  onFilters: (filters: Filter[]) => void;
}) {
  const [settings, setSettings] = useState(false);
  // A filter from the URL finds its label in the dashboard's rows, if they
  // are loaded.
  const queryClient = useQueryClient();
  const stats = queryClient.getQueryData<SiteStats>(
    siteStatsQuery(ref.project, site.name, range, filters).queryKey,
  );
  const hostnames = site.hostnames ?? [];
  const services = new Set(hostnames.map((h) => h.service).filter(Boolean));
  const { org = "" } = useParams({ strict: false });
  const params = {
    org,
    project: ref.project,
    environment: ref.environment,
    service: ref.service,
  };
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {sites.length > 1 ? (
          <select
            aria-label="Site"
            value={site.name}
            onChange={(e) => onSite(e.target.value)}
          >
            {sites.map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="font-semibold">{site.name}</span>
        )}
        <span className="text-xs text-faint">
          site · {hostnames.length}{" "}
          {hostnames.length === 1 ? "hostname" : "hostnames"} · {services.size}{" "}
          {services.size === 1 ? "service" : "services"}
        </span>
        <Button onClick={() => setSettings(true)}>Settings</Button>
        {site.public_urls?.[0] && (
          <a
            href={site.public_urls[0]}
            target="_blank"
            rel="noreferrer"
            className="text-sm text-info"
          >
            public ↗
          </a>
        )}
        <span className="ml-auto">
          <RangeSwitch value={range} options={statsRanges} onChange={onRange} />
        </span>
      </div>
      <nav
        aria-label="Analytics"
        className="flex gap-4 border-b border-line text-sm"
      >
        {views.map(([label, to]) => (
          <Link
            key={to}
            to={to}
            params={params}
            search={searchOf(site.name, range, filters)}
            aria-current={to === view ? "page" : undefined}
            className={`-mb-px border-b-2 pb-1.5 ${to === view ? "border-fg text-fg" : "border-transparent text-mut"}`}
          >
            {label}
          </Link>
        ))}
      </nav>
      {filters.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {filters.map((f) => (
            <button
              key={`${f.dimension}:${f.op}:${f.value}`}
              type="button"
              className="rounded-ctl bg-info-bg px-2 py-0.5 text-xs text-fg ring-1 ring-info/30 ring-inset"
              title="Remove this filter"
              onClick={() => onFilters(filters.filter((g) => g !== f))}
            >
              <span className="text-info">{f.dimension.replace("_", " ")}</span>{" "}
              {f.op.replace("_", " ")}{" "}
              {shown(f.dimension, {
                value: f.value,
                label:
                  labels[`${f.dimension}:${f.value}`] ??
                  stats?.breakdowns[f.dimension]?.find(
                    (r) => r.value === f.value,
                  )?.label,
              })}{" "}
              <span aria-hidden>✕</span>
            </button>
          ))}
          <button
            type="button"
            className="text-xs text-info"
            onClick={() => onFilters([])}
          >
            Clear all
          </button>
        </div>
      )}
      {settings && (
        <Suspense fallback={null}>
          <SiteSettingsDialog
            project={ref.project}
            site={site}
            onRenamed={onRenamed}
            onClose={() => setSettings(false)}
          />
        </Suspense>
      )}
    </div>
  );
}
