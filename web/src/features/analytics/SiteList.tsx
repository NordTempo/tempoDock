import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { useOrg } from "../../shell/location";
import { Icon } from "../../ui/icons";
import { SectionHead } from "../../ui/kit";
import { type OrgSitesRange, orgSitesRanges } from "./loaders";
import { delta } from "./parts";

// The Analytics area's sites (ADR 0059) in two views the page bar
// switches between: rows by project, or tiles. Each site shows its
// visitors and page views over the range with the change against the
// range before, and its visitors as a sparkline.

type OrgSite = components["schemas"]["OrgSite"];

export const siteViews = ["rows", "tiles"] as const;
export type SiteView = (typeof siteViews)[number];

const viewKey = "dock.analytics.view";
const rangeKey = "dock.analytics.range";

/**
 * The range last chosen on the site list on this browser, 24h until one
 * is: the list opens on it when its URL names none.
 */
export function lastSitesRange(): OrgSitesRange {
  const saved = localStorage.getItem(rangeKey);
  return orgSitesRanges.find((r) => r === saved) ?? "24h";
}

/** Saves the site list's range for the next visit. */
export const rememberSitesRange = (range: OrgSitesRange) =>
  localStorage.setItem(rangeKey, range);

/** The view last chosen on this browser, rows until one is. */
export function useSiteView() {
  const [view, setView] = useState<SiteView>(() =>
    localStorage.getItem(viewKey) === "tiles" ? "tiles" : "rows",
  );
  const choose = (v: SiteView) => {
    localStorage.setItem(viewKey, v);
    setView(v);
  };
  return [view, choose] as const;
}

/** Rows or tiles, two icon buttons beside the range switch. */
export function ViewSwitch({
  value,
  onChange,
}: {
  value: SiteView;
  onChange: (v: SiteView) => void;
}) {
  return (
    <fieldset className="seg" aria-label="View">
      {siteViews.map((v) => (
        <button
          key={v}
          type="button"
          aria-pressed={v === value}
          aria-label={v === "rows" ? "Rows" : "Tiles"}
          title={v === "rows" ? "Rows" : "Tiles"}
          onClick={() => onChange(v)}
        >
          <Icon name={v === "rows" ? "list" : "grid"} size={14} />
        </button>
      ))}
    </fieldset>
  );
}

const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});

/** Rows grouped by project, in the API's order. */
export function SiteRows({
  sites,
  range,
}: {
  sites: OrgSite[];
  range: OrgSitesRange;
}) {
  const projects = new Map<string, OrgSite[]>();
  for (const s of sites)
    projects.set(s.project, [...(projects.get(s.project) ?? []), s]);
  return [...projects].map(([project, rows]) => (
    <section key={project} aria-labelledby={`p-${project}`}>
      <SectionHead id={`p-${project}`}>{project}</SectionHead>
      <ul className="border-t border-line">
        {rows.map((s) => (
          <li key={s.site.name} className="border-b border-soft">
            <SiteLink
              site={s}
              range={range}
              className="grid grid-cols-2 items-center gap-x-5 gap-y-2 px-2 py-3 hover:bg-hover sm:grid-cols-[minmax(0,1fr)_minmax(120px,280px)_130px_130px_14px] sm:gap-x-6"
            >
              <span className="col-span-2 flex min-w-0 items-center gap-2.5 sm:col-span-1">
                <SiteGlyph name={s.site.name} />
                <span className="min-w-0">
                  <span className="block truncate font-mono text-md text-info underline-offset-3 group-hover:underline">
                    {s.site.name}
                  </span>
                  <span className="block truncate text-xs text-faint">
                    {[s.site.environment, ...services(s)].join(" · ")}
                  </span>
                </span>
              </span>
              <Sparkline
                series={s.series}
                className="h-10 w-full max-sm:hidden"
              />
              <Metric
                label="Visitors"
                site={s}
                metric="visitors"
                range={range}
              />
              <Metric
                label="Page views"
                site={s}
                metric="pageviews"
                range={range}
              />
              <span className="max-sm:hidden">
                <Icon name="chevronRight" size={14} />
              </span>
            </SiteLink>
          </li>
        ))}
      </ul>
    </section>
  ));
}

/** Tiles, every site in one grid with its project under its line. */
export function SiteTiles({
  sites,
  range,
}: {
  sites: OrgSite[];
  range: OrgSitesRange;
}) {
  return (
    <ul className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
      {sites.map((s) => (
        <li key={`${s.project}/${s.site.name}`}>
          <SiteLink
            site={s}
            range={range}
            className="flex flex-col gap-3 rounded-panel border border-line bg-panel px-3.5 pt-3 pb-2.5 hover:bg-hover"
          >
            <span className="flex min-w-0 items-center gap-2.5">
              <SiteGlyph name={s.site.name} />
              <span className="min-w-0 flex-1 truncate font-mono text-fg">
                {s.site.name}
              </span>
              <Icon name="chevronRight" size={14} />
            </span>
            <span className="flex gap-7">
              <Metric
                label="Visitors"
                site={s}
                metric="visitors"
                range={range}
              />
              <Metric
                label="Page views"
                site={s}
                metric="pageviews"
                range={range}
              />
            </span>
            <Sparkline series={s.series} className="h-14 w-full" />
            <span className="flex justify-between gap-3 text-xs text-faint">
              <span className="truncate">
                {s.project} · {s.site.environment}
              </span>
              <span>{range}</span>
            </span>
          </SiteLink>
        </li>
      ))}
    </ul>
  );
}

/** A site's page on the list's range, as a block. */
function SiteLink({
  site: { project, site },
  range,
  className,
  children,
}: {
  site: OrgSite;
  range: OrgSitesRange;
  className: string;
  children: ReactNode;
}) {
  const org = useOrg();
  return (
    <Link
      to="/$org/analytics/$project/$site"
      params={{ org, project, site: site.name }}
      search={{ range }}
      // group: the site's name underlines with the row, so the row reads as a link.
      className={`group text-faint hover:text-fg ${className}`}
    >
      {children}
    </Link>
  );
}

/** The services a site's hostnames serve. */
const services = (s: OrgSite) => [
  ...new Set((s.site.hostnames ?? []).flatMap((h) => h.service || [])),
];

/** The site's first letter on a chip, where a favicon would go. */
function SiteGlyph({ name }: { name: string }) {
  return (
    <span
      aria-hidden
      className="grid size-6 flex-none place-items-center rounded-md bg-chip text-xs font-semibold text-sec uppercase"
    >
      {name[0]}
    </span>
  );
}

/** A number over the range with its change; a dash while the store is away. */
function Metric({
  label,
  site,
  metric,
  range,
}: {
  label: string;
  site: OrgSite;
  metric: "visitors" | "pageviews";
  range: OrgSitesRange;
}) {
  const now = site.overview?.[metric];
  const change =
    now !== undefined &&
    delta(now, site.previous?.[metric] ?? 0, range, compact.format);
  return (
    <span className="min-w-0 tabular-nums">
      <span className="block text-xs text-mut">{label}</span>
      <span className="flex items-baseline gap-2 whitespace-nowrap">
        <span
          className="text-lg font-semibold text-fg"
          title={now?.toLocaleString("en-US")}
        >
          {now === undefined ? "–" : compact.format(now)}
        </span>
        {change && (
          <span
            className={`truncate text-xs ${change.quiet ? "text-mut" : change.up ? "text-ok" : "text-fail"}`}
            title={change.long}
          >
            {change.text}
          </span>
        )}
      </span>
    </span>
  );
}

/**
 * Visitors per bucket as a line over a faint area, scaled to the busiest
 * bucket and stretched to its box. Nothing while there is no series.
 */
function Sparkline({
  series,
  className,
}: {
  series: OrgSite["series"];
  className: string;
}) {
  if (!series || series.length < 2) return <span className={className} />;
  const max = Math.max(1, ...series.map((p) => p.visitors));
  const w = series.length - 1;
  const line = series
    .map((p, i) => `${i ? "L" : "M"}${i} ${(1 - p.visitors / max) * 29 + 1}`)
    .join("");
  return (
    <svg
      viewBox={`0 0 ${w} 30`}
      preserveAspectRatio="none"
      aria-hidden
      className={`text-info ${className}`}
    >
      <path d={`${line}L${w} 30L0 30Z`} fill="currentColor" opacity={0.12} />
      <path
        d={line}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.25}
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
