import { useSuspenseQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useCanDeploy, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
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
import { SiteBar, type View, views } from "./SiteBar";

// A service's Analytics tab (ADR 0037): the site its hostnames count for,
// with the bar every view shares above the view, or, before any does,
// turning counting on.

/** The script a counted hostname's pages load. */
export const scriptTag = '<script src="/_dock/a/s.js" defer></script>';

export function AnalyticsTab({
  serviceRef: ref,
  view,
  site: picked,
  range,
  filters,
  onChange,
  children,
}: {
  serviceRef: ServiceRef;
  view: View;
  site?: string;
  range: StatsRange;
  filters: Filter[];
  onChange: (change: AnalyticsChange) => void;
  /**
   * The view of the site; onPick filters by a clicked value, keeping its
   * label for the filter's chip.
   */
  children: (
    site: Site,
    onPick: (dimension: string, row: { value: string; label?: string }) => void,
  ) => ReactNode;
}) {
  const { data: all } = useSuspenseQuery(sitesQuery(ref.project));
  const sites = all.filter(
    (s) =>
      !s.archived &&
      s.environment === ref.environment &&
      (s.hostnames ?? []).some((h) => h.service === ref.service),
  );
  const site = sites.find((s) => s.name === picked) ?? sites[0];
  const [labels, setLabels] = useState<Record<string, string>>({});
  const onPick = (
    dimension: string,
    row: { value: string; label?: string },
  ) => {
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
  };
  return (
    <div className="flex flex-col gap-6">
      {site ? (
        <div className="flex flex-col gap-4">
          <SiteBar
            serviceRef={ref}
            sites={sites}
            site={site}
            view={view}
            range={range}
            filters={filters}
            labels={labels}
            onSite={(name) => onChange({ site: name, filters: [] })}
            onRenamed={(name) => onChange({ site: name })}
            onRange={(r) => onChange({ range: r })}
            onFilters={(f) => onChange({ filters: f })}
          />
          {children(site, onPick)}
        </div>
      ) : (
        <p className="text-mut">
          Dock Analytics counts visits to a hostname without cookies. Turn it on
          for one of this service's domains, then add the script to its pages.
        </p>
      )}
      {(!site || view === views[0][1]) && (
        <Hostnames
          serviceRef={ref}
          counted={
            new Map(
              sites.flatMap((s) =>
                (s.hostnames ?? []).map((h) => [h.hostname, s.name]),
              ),
            )
          }
        />
      )}
    </div>
  );
}

/** The service's domains and whether each is counted, with the switch. */
function Hostnames({
  serviceRef: ref,
  counted,
}: {
  serviceRef: ServiceRef;
  /** Counted hostnames and their sites. */
  counted: Map<string, string>;
}) {
  const { domains } = useService(ref);
  const canDeploy = useCanDeploy(ref);
  const enable = useEnableHostname();
  const disable = useDisableHostname();
  const hosts = domains.filter((d) => !d.redirect_to).map((d) => d.hostname);
  if (!hosts.length) {
    return (
      <p className="text-mut">
        This service has no domain yet; add one on Domains.
      </p>
    );
  }
  return (
    <section aria-labelledby="hostnames" className="flex flex-col gap-2">
      <h2 id="hostnames" className="text-sm font-semibold">
        Hostnames
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
                    {site ? `counted for site ${site}` : "not counted"}
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
