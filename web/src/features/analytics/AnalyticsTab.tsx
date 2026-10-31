import { useSuspenseQuery } from "@tanstack/react-query";
import { useCanDeploy, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { Act, CopyButton, ErrorText, Panel } from "../../ui/kit";
import { Dashboard } from "./Dashboard";
import {
  type Filter,
  type StatsRange,
  sitesQuery,
  useDisableHostname,
  useEnableHostname,
} from "./data";

// A service's Analytics tab (ADR 0037): the dashboard of the site its
// hostnames count for, or, before any does, turning counting on.

/** The script a counted hostname's pages load. */
export const scriptTag = '<script src="/_dock/a/s.js" defer></script>';

export function AnalyticsTab({
  serviceRef: ref,
  site: picked,
  range,
  filters,
  onChange,
}: {
  serviceRef: ServiceRef;
  site?: string;
  range: StatsRange;
  filters: Filter[];
  onChange: (change: {
    site?: string;
    range?: StatsRange;
    filters?: Filter[];
  }) => void;
}) {
  const { data: all } = useSuspenseQuery(sitesQuery(ref.project));
  const sites = all.filter(
    (s) =>
      !s.archived &&
      s.environment === ref.environment &&
      (s.hostnames ?? []).some((h) => h.service === ref.service),
  );
  const site = sites.find((s) => s.name === picked) ?? sites[0];
  return (
    <div className="flex flex-col gap-6">
      {site ? (
        <Dashboard
          project={ref.project}
          sites={sites}
          site={site}
          range={range}
          filters={filters}
          onSite={(name) => onChange({ site: name, filters: [] })}
          onRenamed={(name) => onChange({ site: name })}
          onRange={(r) => onChange({ range: r })}
          onFilters={(f) => onChange({ filters: f })}
        />
      ) : (
        <p className="text-mut">
          Dock Analytics counts visits to a hostname without cookies. Turn it on
          for one of this service's domains, then add the script to its pages.
        </p>
      )}
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
