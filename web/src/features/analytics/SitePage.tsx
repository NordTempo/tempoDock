import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link, Outlet, useMatch, useNavigate } from "@tanstack/react-router";
import { createContext, useContext, useState } from "react";
import { canDeploy, useSession } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { Page, PageBar } from "../../ui/kit";
import {
  type AnalyticsChange,
  type AnalyticsRow,
  applyChange,
  errorDimensions,
  type Filter,
  parseFilter,
  type Site,
  type StatsRange,
  siteErrorsQuery,
  sitesQuery,
} from "./data";
import type { AnalyticsSearch } from "./loaders";
import { count } from "./parts";
import { SiteBar } from "./SiteBar";

// A site's page in the Analytics area (ADR 0059, layout C): its name and
// project in the header bar, its views down a side menu (beside the
// project rail: the route's staticData.ownMenu), the range and filters
// they share above the view. Settings is a view too, without the range.
// The views read the site, range and filters from useSiteView.

/** A site's views, down the side menu. */
const views = [
  ["Overview", "/$org/analytics/$project/$site"],
  ["Errors", "/$org/analytics/$project/$site/errors"],
  ["Vitals", "/$org/analytics/$project/$site/vitals"],
  ["Funnels", "/$org/analytics/$project/$site/funnels"],
  ["Replays", "/$org/analytics/$project/$site/replays"],
  ["Settings", "/$org/analytics/$project/$site/settings"],
] as const;

type SiteView = {
  project: string;
  site: Site;
  range: StatsRange;
  filters: Filter[];
  /** May add and remove goals and funnels (deploy in its environment). */
  canEdit: boolean;
  onChange: (change: AnalyticsChange) => void;
  /** Filters by a clicked value, keeping its label for the filter's chip. */
  onPick: (dimension: string, row: AnalyticsRow) => void;
};

const SiteContext = createContext<SiteView | null>(null);

/** The site page's site, range and filters, for its views. */
export function useSiteView() {
  const view = useContext(SiteContext);
  if (!view) throw new Error("useSiteView outside a site page");
  return view;
}

export function SitePage({
  project,
  name,
  search,
}: {
  project: string;
  name: string;
  search: AnalyticsSearch;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const session = useSession();
  const site = useSuspenseQuery({
    ...sitesQuery(project),
    select: (all) => all.find((s) => s.name === name),
  }).data;
  const [labels, setLabels] = useState<Record<string, string>>({});
  // Settings has no range or filters: no bar above it.
  const onSettings = !!useMatch({
    from: "/_app/$org/analytics/$project/$site/settings",
    shouldThrow: false,
  });
  if (!site) {
    return (
      <Page>
        <p className="text-mut">
          No site {name} in project {project}.{" "}
          <Link to="/$org/analytics" params={{ org }}>
            All sites
          </Link>
        </p>
      </Page>
    );
  }
  const range = search.range ?? "30d";
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const onChange = (change: AnalyticsChange) => {
    // The view's own search (a vital, a funnel) stays.
    const { range, f } = applyChange(search, change);
    navigate({ to: ".", search: (s) => ({ ...s, range, f }), replace: true });
  };
  const view: SiteView = {
    project,
    site,
    range,
    filters,
    canEdit: canDeploy(session, { project, environment: site.environment }),
    onChange,
    onPick: (dimension, row) => {
      if (row.label)
        setLabels((l) => ({
          ...l,
          [`${dimension}:${row.value}`]: row.label ?? "",
        }));
      onChange({
        filters: [
          ...filters.filter((f) => f.dimension !== dimension),
          { dimension, op: "is", value: row.value },
        ],
      });
    },
  };
  // Switching view keeps the range and filters.
  const kept = { range: search.range, f: search.f };
  return (
    <>
      <PageBar
        parent={
          <Link to="/$org/analytics" params={{ org }} className="hover:text-fg">
            Analytics
          </Link>
        }
        title={<span className="font-mono">{site.name}</span>}
      />
      <div className="flex flex-1">
        <nav
          aria-label="Site"
          className="hidden w-[150px] flex-none border-r border-line px-2 py-4 sm:block"
        >
          <div className="sticky top-[calc(var(--shell-top)+80px)] flex flex-col gap-px">
            {views.map(([label, to]) => (
              <Link
                key={to}
                to={to}
                params={{ org, project, site: site.name }}
                search={kept}
                activeOptions={{
                  exact: to === views[0][1],
                  includeSearch: false,
                }}
                className="flex min-h-[28px] items-center gap-2 rounded-ctl px-2 py-1 text-mut hover:text-fg aria-[current=page]:bg-sel aria-[current=page]:text-fg"
              >
                <span className="flex-1">{label}</span>
                {label === "Errors" && (
                  <ErrorCount
                    project={project}
                    site={site.name}
                    range={range}
                    filters={filters}
                  />
                )}
              </Link>
            ))}
          </div>
        </nav>
        <Page>
          <nav aria-label="Site" className="flex gap-3 text-sm sm:hidden">
            {views.map(([label, to]) => (
              <Link
                key={to}
                to={to}
                params={{ org, project, site: site.name }}
                search={kept}
                activeOptions={{
                  exact: to === views[0][1],
                  includeSearch: false,
                }}
                className="text-mut aria-[current=page]:text-fg"
              >
                {label}
              </Link>
            ))}
          </nav>
          {!onSettings && (
            <SiteBar
              project={project}
              named={false}
              site={site}
              range={range}
              filters={filters}
              labels={labels}
              onRange={(r) => onChange({ range: r })}
              onFilters={(f) => onChange({ filters: f })}
            />
          )}
          <SiteContext.Provider value={view}>
            <Outlet />
          </SiteContext.Provider>
        </Page>
      </div>
    </>
  );
}

/**
 * How many errors the site had in the range, beside its Errors view;
 * the same query the view reads, so opening it costs nothing more.
 */
function ErrorCount({
  project,
  site,
  range,
  filters,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
}) {
  const usable = filters.filter((f) =>
    (errorDimensions as readonly string[]).includes(f.dimension),
  );
  const errors = useQuery(siteErrorsQuery(project, site, range, usable)).data
    ?.summary.errors;
  if (!errors) return null;
  return (
    <span className="text-2xs text-fail tabular-nums">
      {count(errors)}
      <span className="sr-only"> errors</span>
    </span>
  );
}
