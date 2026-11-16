import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useOrg } from "../../shell/location";
import { RangeSwitch } from "../stats/parts";
import {
  type Filter,
  type Site,
  type SiteStats,
  type StatsRange,
  siteStatsQuery,
  siteTrackingQuery,
  statsRanges,
} from "./data";
import { shown } from "./parts";
import { TrackingChecklist, TrackingChip, trackingOpens } from "./Tracking";

// The bar above every analytics view (ADR 0037): the site (a choice of
// sites on a service's Errors tab), whether its tracking works, the range
// and the filters they share. The site's settings are a page in the
// Analytics area; a service's view links to it.

export function SiteBar({
  project,
  sites = [],
  site,
  range,
  filters,
  labels,
  onSite,
  onRange,
  onFilters,
  named = true,
}: {
  project: string;
  /**
   * Show the site's name (or the choice of sites) and a link to its
   * settings; a site page has both in its header and menu.
   */
  named?: boolean;
  /** The sites to choose from, when there are others. */
  sites?: Site[];
  site: Site;
  range: StatsRange;
  filters: Filter[];
  /** Labels of filtered values (services, releases) from rows clicked. */
  labels: Record<string, string>;
  onSite?: (name: string) => void;
  onRange: (range: StatsRange) => void;
  onFilters: (filters: Filter[]) => void;
}) {
  const org = useOrg();
  // A filter from the URL finds its label in the dashboard's rows, if they
  // are loaded.
  const queryClient = useQueryClient();
  const stats = queryClient.getQueryData<SiteStats>(
    siteStatsQuery(project, site.name, range, filters).queryKey,
  );
  const hostnames = site.hostnames ?? [];
  // The checklist opens by itself when something is wrong, until toggled.
  const tracking = useQuery(siteTrackingQuery(project, site.name)).data;
  const [checklist, setChecklist] = useState<boolean | null>(null);
  const showChecklist = checklist ?? trackingOpens(tracking);
  const services = new Set(hostnames.map((h) => h.service).filter(Boolean));
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {!named ? null : sites.length > 1 ? (
          <select
            aria-label="Site"
            value={site.name}
            onChange={(e) => onSite?.(e.target.value)}
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
        <TrackingChip
          project={project}
          site={site.name}
          open={showChecklist}
          onToggle={() => setChecklist(!showChecklist)}
        />
        {site.privacy.needs_consent && (
          <Link
            to="/$org/analytics/$project/$site/settings"
            params={{ org, project, site: site.name }}
            hash="set-privacy"
            title={`${site.privacy.summary} ${site.privacy.detail ?? ""}`}
            className="text-sm text-warn-text hover:underline"
          >
            ▲ needs consent: replay
          </Link>
        )}
        {named && (
          <Link
            to="/$org/analytics/$project/$site/settings"
            params={{ org, project, site: site.name }}
            className="btn sm"
          >
            Site settings
          </Link>
        )}
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
      {showChecklist && (
        <TrackingChecklist project={project} site={site.name} />
      )}
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
    </div>
  );
}
