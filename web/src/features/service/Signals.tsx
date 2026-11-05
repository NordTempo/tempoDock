import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, elapsed } from "../../ui/format";
import { orgSitesQuery } from "../analytics/loaders";
import { uptimeChecks } from "../uptime/data";

// The service Overview's lines on how it does from outside (ADR 0059):
// its uptime and last incident, and its site's visitors with a link to
// the site in the Analytics area. Each shows only when there is one.

const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});

/** A link at least 24px tall (WCAG 2.5.8), on the line's text. */
const target = "inline-flex min-h-[24px] items-center";

const stateWord = { up: "Up", down: "Down", unknown: "Not probed yet" };
const stateClass = { up: "text-ok", down: "text-fail", unknown: "text-mut" };

export function Signals({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { uptime, domains } = useService(ref);
  if (!uptime && domains.length === 0) return null;
  return (
    <div className="flex flex-col gap-1 text-xs">
      {uptime && <UptimeLine serviceRef={ref} />}
      {domains.length > 0 && <VisitorsLine serviceRef={ref} />}
    </div>
  );
}

/** Up or down, the last day's uptime and the latest incident. */
function UptimeLine({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { uptime } = useService(ref);
  // The checks carry the incidents; the Uptime tab shares the query.
  const checks = useQuery(uptimeChecks(ref)).data ?? [];
  const last = checks
    .flatMap((c) => c.incidents)
    .sort((a, b) => b.opened_at.localeCompare(a.opened_at))[0];
  if (!uptime) return null;
  return (
    <p className="flex flex-wrap items-center gap-x-1.5">
      <span className="text-faint">Uptime</span>
      <span className={stateClass[uptime.state]}>
        {stateWord[uptime.state]}
      </span>
      {uptime.uptime_24h !== undefined && (
        <span className="tabular-nums">
          · {Math.min(uptime.uptime_24h, 99.99).toFixed(2)}% in 24 h
        </span>
      )}
      {last && (
        <span className="text-mut">
          ·{" "}
          {last.resolved_at
            ? `last incident ${ago(last.opened_at)}, down ${elapsed(Date.parse(last.resolved_at) - Date.parse(last.opened_at))}`
            : `down since ${ago(last.opened_at)}`}
        </span>
      )}
      <Link
        to="/$org/$project/$environment/$service/uptime"
        params={{ org, ...ref }}
        className={target}
      >
        Uptime →
      </Link>
    </p>
  );
}

/** The site the service's hostnames count for: visitors in 24 hours. */
function VisitorsLine({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const found = useQuery({
    ...orgSitesQuery(),
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
    <p className="flex flex-wrap items-center gap-x-1.5">
      <span className="text-faint">Analytics</span>
      <Link
        to="/$org/analytics/$project/$site"
        params={{ org, project: ref.project, site: found.site.name }}
        className={target}
      >
        {found.last_24h
          ? `${compact.format(found.last_24h.visitors)} visitors in 24 h`
          : found.site.name}{" "}
        →
      </Link>
    </p>
  );
}
