import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Act, Button, ErrorText, Panel } from "../../ui/kit";
import {
  type Filter,
  type StatsRange,
  siteGoalsQuery,
  useAddGoal,
  useRemoveGoal,
} from "./data";
import { count } from "./parts";

// A site's goals on its Overview (ADR 0037, phase A4; Saile's pick: C):
// each goal's converted visitors and their share of the range's, next to
// Pages. "+ Goal" turns the panel's last row into the form.

/** The CLI command a goal's form stands for. */
const cli = (site: string, name: string, kind: string, match: string) =>
  `dock analytics goals add ${site} ${JSON.stringify(name || "Name")} ${kind}:${match || (kind === "page" ? "/path" : "event")}`;

export function GoalsPanel({
  project,
  site,
  range,
  filters,
  canEdit,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
  canEdit: boolean;
}) {
  const { data, error } = useQuery(
    siteGoalsQuery(project, site, range, filters),
  );
  const [adding, setAdding] = useState(false);
  const remove = useRemoveGoal();
  const goals = data?.goals ?? [];
  return (
    <section aria-label="Goals" className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-baseline gap-3">
        <h3 className="text-sm font-semibold">Goals</h3>
        {canEdit && !adding && (
          <button
            type="button"
            className="text-xs text-mut"
            onClick={() => setAdding(true)}
          >
            + Goal
          </button>
        )}
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th className="sr-only">Goal</th>
              <th className="w-20 text-right">Visitors</th>
              <th className="w-16 text-right">Rate</th>
              {canEdit && <th className="w-6" />}
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
                {canEdit && (
                  <td className="text-right">
                    <button
                      type="button"
                      aria-label={`Remove goal ${g.name}`}
                      title={`Remove goal ${g.name}`}
                      className="text-faint"
                      disabled={remove.isPending}
                      onClick={() =>
                        remove.mutate({ project, site, goal: g.name })
                      }
                    >
                      ✕
                    </button>
                  </td>
                )}
              </tr>
            ))}
            {!adding && goals.length === 0 && (
              <tr>
                <td colSpan={4} className="text-mut">
                  {data
                    ? "No goals yet: a page or a custom event that counts as a conversion."
                    : error
                      ? error.message
                      : "Loading…"}
                </td>
              </tr>
            )}
            {adding && (
              <tr>
                <td colSpan={4}>
                  <GoalForm
                    project={project}
                    site={site}
                    onDone={() => setAdding(false)}
                  />
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
      {data && !data.store_online && (
        <p className="text-xs text-warn-text">{data.store_error}</p>
      )}
      <ErrorText error={remove.error} />
    </section>
  );
}

/** A new goal: its name, page or event, and what it matches. */
function GoalForm({
  project,
  site,
  onDone,
}: {
  project: string;
  site: string;
  onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"page" | "event">("event");
  const [match, setMatch] = useState("");
  const add = useAddGoal();
  return (
    <form
      className="flex flex-col gap-2 py-1"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { project, site, goal: { name, kind, match } },
          { onSuccess: onDone },
        );
      }}
    >
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <input
          // biome-ignore lint/a11y/noAutofocus: opened on request
          autoFocus
          aria-label="Goal name"
          placeholder="Signed up"
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="field w-32 py-0.5"
        />
        <span className="seg">
          {(["page", "event"] as const).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={kind === k}
              onClick={() => setKind(k)}
            >
              {k === "page" ? "Page" : "Event"}
            </button>
          ))}
        </span>
        <input
          aria-label={kind === "page" ? "Page path" : "Event name"}
          placeholder={
            kind === "page" ? "/account/welcome (* for any)" : "signup"
          }
          value={match}
          onChange={(e) => setMatch(e.target.value)}
          className="field w-48 py-0.5 font-mono"
        />
        <Act
          type="submit"
          cli={cli(site, name, kind, match)}
          disabled={!name || !match || add.isPending}
        >
          Add goal
        </Act>
        <Button onClick={onDone}>Cancel</Button>
      </div>
      <ErrorText error={add.error} />
    </form>
  );
}
