import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { Act, Button, Confirm, ErrorText } from "../../ui/kit";
import {
  type Filter,
  type Funnel,
  type GoalStep,
  type StatsRange,
  siteFunnelQuery,
  siteFunnelsQuery,
  useAddFunnel,
  useRemoveFunnel,
} from "./data";
import { count } from "./parts";

// A site's funnels (ADR 0037, phase A4; Saile's pick: C): steps a visit
// takes in order, as columns with what is left after each, and the step
// editor with the CLI command it stands for.

export function FunnelsView({
  project,
  site,
  range,
  filters,
  selected,
  onSelect,
  canEdit,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
  selected?: string;
  onSelect: (name: string | undefined) => void;
  canEdit: boolean;
}) {
  const {
    data: funnels,
    error,
    refetch,
  } = useQuery(siteFunnelsQuery(project, site));
  const [editing, setEditing] = useState<Funnel | "new">();
  const remove = useRemoveFunnel();
  const [asking, setAsking] = useState(false);
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!funnels) return <p className="text-mut">Loading…</p>;
  const current = funnels.find((f) => f.name === selected) ?? funnels[0];
  return (
    <div className="flex flex-col gap-4">
      {/* The funnels to pick from, with the shown one's Edit and Remove beside them. */}
      <div className="flex flex-wrap items-center gap-3 text-sm">
        {funnels.map((f) => (
          <button
            key={f.name}
            type="button"
            aria-pressed={f === current}
            className={`min-h-[24px] ${f === current ? "font-semibold text-fg" : "text-mut hover:text-fg"}`}
            onClick={() => onSelect(f.name)}
          >
            {f.name}
          </button>
        ))}
        {canEdit && !editing && current && (
          <span className="flex gap-2">
            <Button small onClick={() => setEditing(current)}>
              Edit
            </Button>
            <Act
              small
              danger
              cli={`dock analytics funnels remove ${site} ${JSON.stringify(current.name)}`}
              disabled={remove.isPending}
              onClick={() => setAsking(true)}
            >
              Remove
            </Act>
          </span>
        )}
        {canEdit && !editing && (
          <Button small onClick={() => setEditing("new")}>
            New funnel
          </Button>
        )}
      </div>
      {editing ? (
        <Editor
          project={project}
          site={site}
          funnel={editing === "new" ? undefined : editing}
          onDone={(name) => {
            setEditing(undefined);
            if (name) onSelect(name);
          }}
        />
      ) : current ? (
        <>
          <Chart
            project={project}
            site={site}
            funnel={current}
            range={range}
            filters={filters}
          />
          {asking && (
            <Confirm
              danger
              action={`Remove ${current.name}`}
              cli={`dock analytics funnels remove ${site} ${JSON.stringify(current.name)}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(
                  { project, site, funnel: current.name },
                  {
                    onSuccess: () => {
                      setAsking(false);
                      onSelect(undefined);
                    },
                  },
                )
              }
              onCancel={() => setAsking(false)}
            >
              Its steps go. The visits and events it counted stay in analytics.
            </Confirm>
          )}
          <ErrorText error={remove.error} />
        </>
      ) : (
        <p className="text-mut">
          No funnels yet. A funnel is two to eight steps, pages or custom
          events, that a visit takes in order; Dock counts how many visits get
          through each.
        </p>
      )}
    </div>
  );
}

/** The funnel over the range: a column per step, what is left after it. */
function Chart({
  project,
  site,
  funnel,
  range,
  filters,
}: {
  project: string;
  site: string;
  funnel: Funnel;
  range: StatsRange;
  filters: Filter[];
}) {
  const { data, error, refetch } = useQuery(
    siteFunnelQuery(project, site, funnel.name, range, filters),
  );
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!data) return <p className="text-mut">Loading…</p>;
  const steps = data.steps ?? [];
  const first = steps[0]?.sessions ?? 0;
  const last = steps.at(-1);
  return (
    <section
      aria-label={`Funnel ${funnel.name}`}
      className="flex flex-col gap-3"
    >
      <p className="text-sm">
        <span className="text-mut">
          {steps.length} steps, in order, within one visit
          {last &&
            first > 0 &&
            ` · ${pct(last.sessions, first)} of ${count(first)} visits converted overall`}
        </span>
      </p>
      {!data.store_online && (
        <p className="text-warn-text">{data.store_error}</p>
      )}
      <ol
        className="grid gap-3"
        style={{
          gridTemplateColumns: `repeat(${Math.max(steps.length, 1)}, minmax(0, 1fr))`,
        }}
      >
        {steps.map((s, i) => {
          const prev = i > 0 ? steps[i - 1]?.sessions : undefined;
          return (
            <li
              key={`${s.kind}:${s.match}`}
              className="flex min-w-0 flex-col gap-1"
            >
              {/* The step names its column, above its bar and numbers. */}
              <span className="truncate text-xs" title={`${s.kind} ${s.match}`}>
                {i + 1}. <span className="text-mut">{s.kind}</span>{" "}
                <span className="font-mono">{s.match}</span>
              </span>
              <div className="relative h-36 rounded-ctl bg-hover">
                {/* A nonzero count always shows a bar you can see. */}
                <div
                  className="absolute inset-x-0 bottom-0 rounded-ctl bg-info/40"
                  style={{
                    height: `${first ? (s.sessions / first) * 100 : 0}%`,
                    minHeight: s.sessions > 0 ? 8 : 0,
                  }}
                  title={`${count(s.sessions)} visits`}
                />
              </div>
              <span className="text-sm font-semibold tabular-nums">
                {count(s.sessions)}
              </span>
              {/* Each share names its basis: the step before, and the first. */}
              <span className="truncate text-xs text-mut tabular-nums">
                {prev !== undefined && prev > 0 && first > 0
                  ? `${pct(s.sessions, prev)} of step ${i} · ${pct(s.sessions, first)} overall`
                  : "\u00a0"}
              </span>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** "3.6%": a share with one decimal. */
const pct = (n: number, of: number) => `${((n / of) * 100).toFixed(1)}%`;

/** A funnel's steps being written: a new one, or an old one replaced. */
function Editor({
  project,
  site,
  funnel,
  onDone,
}: {
  project: string;
  site: string;
  funnel?: Funnel;
  onDone: (saved?: string) => void;
}) {
  const [name, setName] = useState(funnel?.name ?? "");
  const [steps, setSteps] = useState<GoalStep[]>(
    funnel?.steps ?? [
      { kind: "page", match: "" },
      { kind: "event", match: "" },
    ],
  );
  const add = useAddFunnel();
  const remove = useRemoveFunnel();
  const restore = useAddFunnel();
  const set = (i: number, step: Partial<GoalStep>) =>
    setSteps((s) => s.map((x, j) => (j === i ? { ...x, ...step } : x)));
  const ready = name && steps.length >= 2 && steps.every((s) => s.match);
  const cli = `dock analytics funnels add ${site} ${JSON.stringify(name || "Name")} ${steps.map((s) => `${s.kind}:${s.match || "?"}`).join(" ")}`;
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={async (e) => {
        e.preventDefault();
        // Funnels are replaced whole: the old one goes, the new one comes.
        // If the new one is refused, the old one is put back.
        if (funnel)
          await remove.mutateAsync({ project, site, funnel: funnel.name });
        add.mutate(
          { project, site, funnel: { name, steps } },
          {
            onSuccess: () => onDone(name),
            onError: () => {
              if (funnel)
                restore.mutate({
                  project,
                  site,
                  funnel: { name: funnel.name, steps: funnel.steps },
                });
            },
          },
        );
      }}
    >
      <label className="flex items-center gap-2 text-xs">
        <span className="w-14 text-mut">Funnel</span>
        <input
          // biome-ignore lint/a11y/noAutofocus: opened on request
          autoFocus
          value={name}
          placeholder="Checkout"
          onChange={(e) => setName(e.target.value)}
          className="field w-48 py-0.5"
        />
      </label>
      <ol className="flex flex-col gap-1.5">
        {steps.map((s, i) => (
          // Steps have no ID; their place is who they are.
          <li key={i} className="flex flex-wrap items-center gap-2 text-xs">
            <span className="w-14 text-mut">{i + 1}</span>
            <span className="seg">
              {(["page", "event"] as const).map((k) => (
                <button
                  key={k}
                  type="button"
                  aria-pressed={s.kind === k}
                  onClick={() => set(i, { kind: k })}
                >
                  {k === "page" ? "Page" : "Event"}
                </button>
              ))}
            </span>
            <input
              aria-label={`Step ${i + 1}`}
              placeholder={s.kind === "page" ? "/products/*" : "purchase"}
              value={s.match}
              onChange={(e) => set(i, { match: e.target.value })}
              className="field w-56 py-0.5 font-mono"
            />
            {steps.length > 2 && (
              <button
                type="button"
                aria-label={`Remove step ${i + 1}`}
                className="min-h-[24px] min-w-[24px] text-faint hover:text-fg"
                onClick={() => setSteps((x) => x.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            )}
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        {steps.length < 8 && (
          <Button
            onClick={() => setSteps((x) => [...x, { kind: "page", match: "" }])}
          >
            + Step
          </Button>
        )}
        <span className="text-faint">Pages take * for any part.</span>
      </div>
      <div className="flex gap-2">
        <Act
          type="submit"
          cli={cli}
          disabled={!ready || add.isPending || remove.isPending}
        >
          Save funnel
        </Act>
        <Button onClick={() => onDone()}>Cancel</Button>
      </div>
      <ErrorText error={add.error ?? remove.error} />
    </form>
  );
}
