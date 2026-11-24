import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, elapsed } from "../../ui/format";
import { orgSitesQuery } from "../analytics/loaders";
import { uptimeChecks } from "../uptime/data";
import { Chevron, FactLabel } from "./facts";

// How the service does from outside (ADR 0059), as two of the Overview's
// fact rows: its uptime and last incident (opens Metrics, where the
// result is; the checks are set up in Settings › Health and uptime), and
// its site's visitors (opens Requests' Visitors). Each shows only when
// there is one.

const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});

const stateWord = { up: "up", down: "down", unknown: "first check pending" };
const stateClass = { up: "text-ok", down: "text-fail", unknown: "text-mut" };

/** Up or down, the last day's uptime and the latest incident. */
export function UptimeFact({
  serviceRef: ref,
  className,
}: {
  serviceRef: ServiceRef;
  className: string;
}) {
  const org = useOrg();
  const { uptime } = useService(ref);
  // The checks carry the incidents; Metrics and Settings share the query.
  const checks = useQuery({ ...uptimeChecks(ref), enabled: !!uptime }).data;
  const last = (checks ?? [])
    .flatMap((c) => c.incidents)
    .sort((a, b) => b.opened_at.localeCompare(a.opened_at))[0];
  if (!uptime) return null;
  return (
    <Link
      to="/$org/$project/$environment/$service/metrics"
      params={{ org, ...ref }}
      hash="uptime"
      className={className}
    >
      <FactLabel>Uptime</FactLabel>
      <span className="truncate">
        <span className={stateClass[uptime.state]}>
          {stateWord[uptime.state]}
        </span>
        {uptime.uptime_24h !== undefined && (
          <span className="tabular-nums">
            {" "}
            · {Math.min(uptime.uptime_24h, 99.99).toFixed(2)}% in 24h
          </span>
        )}
        {last && (
          <span className="text-mut">
            {" "}
            ·{" "}
            {last.resolved_at
              ? `last incident ${ago(last.opened_at)}, down ${elapsed(Date.parse(last.resolved_at) - Date.parse(last.opened_at))}`
              : `down since ${ago(last.opened_at)}`}
          </span>
        )}
      </span>
      <Chevron />
    </Link>
  );
}

/**
 * The site the service's hostnames count for: visitors in 24 hours,
 * opening Requests' Visitors view.
 */
export function VisitorsFact({
  serviceRef: ref,
  className,
}: {
  serviceRef: ServiceRef;
  className: string;
}) {
  const org = useOrg();
  const { domains } = useService(ref);
  const found = useQuery({
    ...orgSitesQuery(),
    enabled: domains.length > 0,
    select: (all) =>
      all.find(
        (s) =>
          s.project === ref.project &&
          s.site.environment === ref.environment &&
          (s.site.hostnames ?? []).some((h) => h.service === ref.service),
      ),
  }).data;
  if (!found) return null;
  return (
    <Link
      to="/$org/$project/$environment/$service/requests/visitors"
      params={{ org, ...ref }}
      search={{ site: found.site.name }}
      className={className}
    >
      <FactLabel>Visitors</FactLabel>
      <span className="truncate">
        {found.overview
          ? `${compact.format(found.overview.visitors)} in 24h`
          : found.site.name}
      </span>
      <Chevron />
    </Link>
  );
}
