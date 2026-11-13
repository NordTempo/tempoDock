import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useOrg } from "../../shell/location";
import { Panel } from "../../ui/kit";
import { type Filter, type StatsRange, siteGoalsQuery } from "./data";
import { count } from "./parts";

// A site's goals on its Overview (ADR 0037, phase A4; Saile's pick: C):
// each goal's converted visitors and their share of the range's, next to
// Pages. Goals are added and removed in the site's Settings.

export function GoalsPanel({
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
  const org = useOrg();
  const { data, error } = useQuery(
    siteGoalsQuery(project, site, range, filters),
  );
  const goals = data?.goals ?? [];
  return (
    <section aria-label="Goals" className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-baseline gap-3">
        <h3 className="text-sm font-semibold">Goals</h3>
        <Link
          to="/$org/analytics/$project/$site/settings"
          params={{ org, project, site }}
          hash="set-goals"
          className="text-xs text-mut hover:text-fg"
        >
          Settings
        </Link>
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th className="sr-only">Goal</th>
              <th className="w-20 text-right">Visitors</th>
              <th className="w-16 text-right">Rate</th>
            </tr>
          </thead>
          <tbody>
            {goals.map((g) => (
              <tr key={g.name}>
                <td className="max-w-0 truncate" title={`${g.kind} ${g.match}`}>
                  {g.name}{" "}
                  <span className="font-mono text-xs text-faint">
                    {g.match}
                  </span>
                </td>
                <td className="text-right">{count(g.visitors)}</td>
                <td className="text-right text-mut">
                  {(g.rate * 100).toFixed(1)}%
                </td>
              </tr>
            ))}
            {goals.length === 0 && (
              <tr>
                <td colSpan={3} className="text-mut">
                  {data
                    ? "No goals yet: a page or a custom event that counts as a conversion."
                    : error
                      ? error.message
                      : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
      {data && !data.store_online && (
        <p className="text-xs text-warn-text">{data.store_error}</p>
      )}
    </section>
  );
}
