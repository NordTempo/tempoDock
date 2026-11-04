import { Link } from "@tanstack/react-router";
import { Fragment, useState } from "react";
import { useProjectOverview, useSession } from "../../data/queries";
import type { ApplyPlan } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { plural } from "../../ui/format";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import { Field, Fields, input } from "../create/form";
import { usePlanFile, useStageFile } from "./data";
import { type PlanRow, planRows } from "./plan";

/**
 * Apply file: `dock apply FILE` from the project overview (ADR 0049).
 * Paste or load a project file, see its plan, and stage it; each
 * environment's staged changes are then reviewed and applied there.
 * Loaded on first open.
 */
export default function ApplyFile({
  project,
  onClose,
}: {
  project: string;
  onClose: () => void;
}) {
  const org = useOrg();
  const session = useSession();
  const { environments } = useProjectOverview(project);
  const [file, setFile] = useState("");
  const [name, setName] = useState(`${project}.dock.toml`);
  // An environment-limited token can only plan and apply its own.
  const [environment, setEnvironment] = useState(session?.environment ?? "");
  const [prune, setPrune] = useState(false);
  const plan = usePlanFile();
  const stage = useStageFile();
  const busy = plan.isPending || stage.isPending;
  const request = {
    project,
    file,
    prune,
    ...(environment.trim() && { environment: environment.trim() }),
  };
  const flags = `${prune ? " --prune" : ""}${request.environment ? ` -e ${request.environment}` : ""}`;
  const cli = `dock apply ${name} -p ${project}`;

  // An edit makes the plan and result shown stale.
  const edit =
    <T,>(set: (value: T) => void) =>
    (value: T) => {
      set(value);
      plan.reset();
      stage.reset();
    };

  const staged = stage.data;
  const shown = staged?.plan ?? plan.data;

  return (
    <Dialog title={`Apply a project file to ${project}`} onClose={onClose} wide>
      <div className="flex flex-col gap-3.5">
        <Fields>
          <Field
            label="File"
            note={
              <label className="btn sm focus-within:ring-1 focus-within:ring-info">
                Load a file
                <input
                  type="file"
                  accept=".toml,.json,application/json,application/toml,text/plain"
                  className="sr-only"
                  onChange={async (e) => {
                    const picked = e.target.files?.[0];
                    if (!picked) return;
                    setName(picked.name);
                    edit(setFile)(await picked.text());
                  }}
                />
              </label>
            }
          >
            <textarea
              value={file}
              onChange={(e) => edit(setFile)(e.target.value)}
              placeholder={`version = 1\nproject = "${project}"\n\n[[environments]]\n  name = "production"`}
              rows={8}
              spellCheck={false}
              className="field w-full resize-y font-mono text-xs"
            />
          </Field>
          <Field label="Environment">
            <input
              value={environment}
              onChange={(e) => edit(setEnvironment)(e.target.value)}
              list="apply-file-environments"
              placeholder="every one in the file"
              spellCheck={false}
              autoComplete="off"
              disabled={!!session?.environment}
              className={input}
            />
          </Field>
          <datalist id="apply-file-environments">
            {environments.map((e) => (
              <option key={e.environment.id} value={e.environment.name} />
            ))}
          </datalist>
          <span />
          <Check
            label="remove what the file doesn't mention (prune)"
            checked={prune}
            onChange={edit(setPrune)}
          />
        </Fields>
        <div className="flex flex-wrap items-center gap-2">
          <Act
            cli={`${cli} --plan${flags}`}
            disabled={!file.trim() || busy}
            onClick={() => plan.mutate(request)}
          >
            Plan
          </Act>
          <Act
            primary
            cli={`${cli}${flags}`}
            disabled={!file.trim() || busy || !!staged}
            onClick={() => stage.mutate(request)}
          >
            Stage
          </Act>
          <Button onClick={onClose}>{staged ? "Done" : "Cancel"}</Button>
          <ErrorText error={plan.error ?? stage.error} />
        </div>
        {staged && (
          <div className="flex flex-col gap-1 text-xs">
            {(staged.staged ?? []).map((p) => (
              <p key={p.environment} className="flex flex-wrap gap-x-3">
                <span className="w-[14ch] font-mono">{p.environment}</span>
                {p.count > 0 ? (
                  <>
                    <span className="text-warn">
                      {plural(p.count, "change")} staged
                    </span>
                    <Link
                      to="/$org/$project/$environment"
                      params={{ org, project, environment: p.environment }}
                      className="lnk"
                      onClick={onClose}
                    >
                      Review and apply
                    </Link>
                  </>
                ) : (
                  <span className="text-mut">nothing staged</span>
                )}
              </p>
            ))}
            <p className="text-mut">
              Nothing goes live until an environment's changes are applied.
            </p>
          </div>
        )}
        {shown && <Plan plan={shown} staged={!!staged} />}
      </div>
    </Dialog>
  );
}

/**
 * A plan like `dock apply --plan` prints it: what staging does, what the
 * file doesn't mention and is kept, and what needs a person. Once staged,
 * only what needs a person is left to show.
 */
function Plan({ plan, staged }: { plan: ApplyPlan; staged: boolean }) {
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
function Rows({ title, rows }: { title: string; rows: PlanRow[] }) {
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
