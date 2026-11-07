import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useCanDeploy, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Act, CopyButton, ErrorText, Panel } from "../../ui/kit";
import {
  type AnalyticsChange,
  type Filter,
  type Site,
  type StatsRange,
  sitesQuery,
  useDisableHostname,
  useEnableHostname,
} from "./data";
import { SiteBar } from "./SiteBar";

// The site a service's hostnames count for (ADR 0037), with the bar every
// analytics view shares, for a view on the service: its Errors tab. The
// site's own pages are in the Analytics area (routes/_app.$org.analytics.*).

/** The script a counted hostname's pages load. */
export const scriptTag = '<script src="/_dock/a/s.js" defer></script>';

/** The live sites a service's hostnames count for in its environment. */
export const sitesOf = (all: Site[], ref: ServiceRef) =>
  all.filter(
    (s) =>
      !s.archived &&
      s.environment === ref.environment &&
      (s.hostnames ?? []).some((h) => h.service === ref.service),
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
        Errors come from Dock Analytics' script on the service's pages. Count
        visits to one of its domains on{" "}
        <Link
          to="/$org/$project/$environment/$service/domains"
          params={{ org, ...ref }}
        >
          Domains
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
        onRenamed={(name) => onChange({ site: name })}
        onRange={(r) => onChange({ range: r })}
        onFilters={(f) => onChange({ filters: f })}
      />
      {children(site)}
    </div>
  );
}

/**
 * The service's domains and whether each is counted, with the switch: on
 * its Domains tab.
 */
export function Hostnames({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { data: all } = useSuspenseQuery(sitesQuery(ref.project));
  const counted = new Map(
    sitesOf(all, ref).flatMap((s) =>
      (s.hostnames ?? []).map((h) => [h.hostname, s.name]),
    ),
  );
  const { domains } = useService(ref);
  const canDeploy = useCanDeploy(ref);
  const enable = useEnableHostname();
  const disable = useDisableHostname();
  const hosts = domains.filter((d) => !d.redirect_to).map((d) => d.hostname);
  if (!hosts.length) {
    return null;
  }
  return (
    <section aria-labelledby="hostnames" className="flex flex-col gap-2">
      <h2 id="hostnames" className="text-md font-semibold">
        Analytics
      </h2>
      <Panel>
        <table>
          <tbody>
            {hosts.map((h) => {
              const site = counted.get(h);
              return (
                <tr key={h}>
                  <td className="font-mono">{h}</td>
                  <td className="text-mut">
                    {site ? (
                      <>
                        counted for site{" "}
                        <Link
                          to="/$org/analytics/$project/$site"
                          params={{ org, project: ref.project, site }}
                        >
                          {site}
                        </Link>
                      </>
                    ) : (
                      "not counted"
                    )}
                  </td>
                  <td className="text-right">
                    {site ? (
                      <Act
                        cli={`dock analytics disable ${h}`}
                        disabled={!canDeploy || disable.isPending}
                        onClick={() => disable.mutate(h)}
                      >
                        Stop counting
                      </Act>
                    ) : (
                      <Act
                        cli={`dock analytics enable ${h}`}
                        disabled={!canDeploy || enable.isPending}
                        onClick={() => enable.mutate({ hostname: h })}
                      >
                        Count visits
                      </Act>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>
      <ErrorText error={enable.error ?? disable.error} />
      <p className="flex flex-wrap items-center gap-2 text-xs text-mut">
        Counted pages load the script, in their {"<head>"}:
        <code className="font-mono text-fg">{scriptTag}</code>
        <CopyButton text={scriptTag} />
      </p>
    </section>
  );
}
