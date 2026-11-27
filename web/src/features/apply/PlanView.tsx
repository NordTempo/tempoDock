import { Fragment } from "react";
import type { ApplyPlan } from "../../data/types";
import { plural } from "../../ui/format";
import { type PlanRow, planRows } from "./plan";

/**
 * A plan like `dock apply --plan` prints it: what staging does, what the
 * file doesn't mention and is kept, and what needs a person. Once staged,
 * only what needs a person is left to show.
 */
export function Plan({ plan, staged }: { plan: ApplyPlan; staged: boolean }) {
  const changes = planRows(plan.changes ?? [], "changes");
  const kept = planRows(plan.kept ?? [], "kept");
  const manual = planRows(plan.manual ?? [], "manual");
  if (staged && manual.length === 0) return null;
  return (
    <div className="flex max-h-[45vh] flex-col gap-2.5 overflow-y-auto border-t border-line pt-3">
      {!staged &&
        (changes.length === 0 ? (
          <p className="text-mut">
            <span className="text-ok">✓</span> No changes: project{" "}
            {plan.project} matches the file.
          </p>
        ) : (
          <Rows title={plural(changes.length, "change")} rows={changes} />
        ))}
      {!staged && kept.length > 0 && (
        <Rows title="Not in the file, kept" rows={kept} />
      )}
      {manual.length > 0 && <Rows title="Needs you" rows={manual} />}
    </div>
  );
}

/** A titled block of plan rows, in the staged changes' row style. */
export function Rows({ title, rows }: { title: string; rows: PlanRow[] }) {
  return (
    <section className="flex flex-col gap-1">
      <h3 className="text-xs font-semibold">{title}</h3>
      <div className="grid grid-cols-[1ch_auto_auto_minmax(0,1fr)] gap-x-3 overflow-x-auto font-mono text-xs leading-[1.9]">
        {rows.map((r) => (
          <Fragment key={r.id}>
            <span className="text-warn">{r.mark}</span>
            <span className="whitespace-nowrap text-faint">{r.where}</span>
            <span className="whitespace-nowrap">{r.what}</span>
            <span className="min-w-0 [overflow-wrap:anywhere]">
              <Detail row={r} />
              {r.note && <span className="text-mut"> {r.note}</span>}
            </span>
          </Fragment>
        ))}
      </div>
    </section>
  );
}

/** old → new, a new value, or a removed one. */
function Detail({ row }: { row: PlanRow }) {
  if (row.old !== undefined && row.new !== undefined) {
    return (
      <>
        <span className="text-fail">{row.old}</span> →{" "}
        <span className="text-ok">{row.new}</span>
      </>
    );
  }
  if (row.new !== undefined) return <span className="text-ok">{row.new}</span>;
  if (row.old !== undefined) {
    return <span className="text-fail line-through">{row.old}</span>;
  }
  return null;
}
