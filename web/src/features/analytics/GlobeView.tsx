import { useState } from "react";
import { BreakdownTable } from "./BreakdownTable";
import {
  type AnalyticsRow,
  type Filter,
  type StatsRange,
  useBreakdown,
} from "./data";
import Globe from "./globe/Globe";

// A site's Globe view: every country its visitors came from as a dot on
// the globe, the top ones listed beside it. Hovering one lights it in
// both; clicking one filters by it.

/** Countries the list beside the globe shows; the globe has them all. */
const LISTED = 20;

export function GlobeView({
  project,
  site,
  range,
  filters,
  onPick,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
  onPick: (dimension: string, row: AnalyticsRow) => void;
}) {
  const [hover, setHover] = useState<string | null>(null);
  const countries = useBreakdown(
    project,
    site,
    range,
    filters,
    "country",
    true,
    250,
  );
  return (
    <div className="flex flex-col gap-4">
      <div className="grid items-start gap-x-8 gap-y-4 lg:grid-cols-[minmax(0,560px)_minmax(0,1fr)]">
        <Globe
          rows={countries.data ?? []}
          hover={hover}
          onHover={setHover}
          onPick={(row) => onPick("country", row)}
        />
        <BreakdownTable
          dimension="country"
          label="Countries"
          rows={countries.data?.slice(0, LISTED)}
          error={countries.error}
          onPick={onPick}
          hover={hover}
          onHover={setHover}
        />
      </div>
      <p className="text-xs text-faint">
        Locations:{" "}
        <a href="https://db-ip.com" target="_blank" rel="noreferrer">
          IP Geolocation by DB-IP
        </a>{" "}
        (CC BY 4.0).
      </p>
    </div>
  );
}
