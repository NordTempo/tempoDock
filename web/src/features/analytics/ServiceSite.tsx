import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import {
  type AnalyticsChange,
  type Filter,
  type Site,
  type StatsRange,
  sitesQuery,
} from "./data";
import { SiteBar } from "./SiteBar";

// The site a service's hostnames count for (ADR 0037), with the bar every
// analytics view shares, for a view on the service: Requests' Visitors.
// The site's other pages are in the Analytics area
// (routes/_app.$org.analytics.*).

/** The script a counted hostname's pages load. */
export const scriptTag = '<script src="/_dock/a/s.js" defer></script>';

/** The service's live site, as a list (at most one, ADR 0104). */
export const sitesOf = (all: Site[], ref: ServiceRef) =>
  all.filter(
    (s) =>
      !s.archived &&
      s.environment === ref.environment &&
      s.service === ref.service,
  );

export function ServiceSite({
  serviceRef: ref,
  site: picked,
  range,
  filters,
  onChange,
  children,
}: {
  serviceRef: ServiceRef;
  site?: string;
  range: StatsRange;
  filters: Filter[];
  onChange: (change: AnalyticsChange) => void;
  /** The view of the site. */
  children: (site: Site) => ReactNode;
}) {
  const org = useOrg();
  const { data: all } = useSuspenseQuery(sitesQuery(ref.project));
  const sites = sitesOf(all, ref);
  const site = sites.find((s) => s.name === picked) ?? sites[0];
  if (!site) {
    return (
      <p className="text-mut">
        Visitors come from Dock Analytics' script on the service's pages. Turn
        counting on in{" "}
        <Link
          to="/$org/$project/$environment/$service/settings"
          params={{ org, ...ref }}
          hash="analytics"
        >
          Settings › Analytics
        </Link>
        , then add the script to its pages.
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <SiteBar
        project={ref.project}
        sites={sites}
        site={site}
        range={range}
        filters={filters}
        labels={{}}
        onSite={(name) => onChange({ site: name, filters: [] })}
        onRange={(r) => onChange({ range: r })}
        onFilters={(f) => onChange({ filters: f })}
      />
      {children(site)}
    </div>
  );
}
