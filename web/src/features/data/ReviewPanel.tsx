import { type ReactNode, useEffect, useState } from "react";
import { plural } from "../../ui/format";
import { useCli, useDataSource } from "./api";
import { Act, Btn, ErrorText } from "./kit";
import type { DataChange } from "./types";
import { dataLimits } from "./types";

/** How staged edits are turned into SQL and committed (see useEditActions). */
export type EditActions = {
  /** The transaction a commit would run (POST /data/changesets, dry_run). */
  renderSql: (changes: DataChange[]) => Promise<string>;
  /**
   * Commit, or propose for callers with level propose. `confirm` is the
   * source's confirmName (an environment's or connection's name) on
   * production.
   */
  commit: (changes: DataChange[], confirm?: string) => Promise<void>;
};

const cellText = (v: string | null | undefined) =>
  v === null || v === undefined ? "NULL" : v;

/**
 * A changeset's rows as cards (G4): the row's key and what happens to it,
 * old value in red and new in green per column.
 */
export function ChangeList({
  changes,
  onRestore,
}: {
  changes: readonly DataChange[];
  /** Unstage one row (while reviewing staged edits). */
  onRestore?: (key: Record<string, string>) => void;
}) {
  return (
    <ul className="flex flex-col gap-2">
      {changes.map((c, i) => {
        const key = Object.values(c.key ?? {}).join(", ");
        const columns = Object.keys(
          (c.op === "delete" ? c.old : c.new) ?? c.key ?? {},
        );
        return (
          <li
            key={`${c.table}${c.op}${key || i}`}
            className="overflow-hidden rounded-lg border border-(--g-line)"
          >
            <div className="flex h-[30px] items-center gap-2 bg-(--g-raise) px-2.5 text-xs text-(--g-dim)">
              <span className="truncate" title={c.table}>
                {c.table.replace(/^public\./, "")} {key}
              </span>
              <span
                className={
                  c.op === "delete" ? "text-(--g-fail)" : "text-(--g-mut)"
                }
              >
                {c.op}
              </span>
              {onRestore && c.key && (
                <button
                  type="button"
                  className="ml-auto text-(--g-mut) hover:text-(--g-text)"
                  onClick={() => c.key && onRestore(c.key)}
                >
                  Undo
                </button>
              )}
            </div>
            {c.op !== "delete" && (
              <div className="g-diff">
                {columns.map((column) => (
                  <div key={column} className="break-all">
                    <span className="text-(--g-mut)">{column}</span>
                    {c.op === "update" && (
                      <div className="g-del">− {cellText(c.old?.[column])}</div>
                    )}
                    <div className="g-add">+ {cellText(c.new?.[column])}</div>
                  </div>
                ))}
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** Generated SQL, as the server rendered it. */
export function SqlText({ children }: { children: ReactNode }) {
  return (
    <pre className="max-h-60 overflow-auto rounded-lg bg-(--g-raise) p-2.5 text-[11.5px] leading-[19px] whitespace-pre-wrap text-(--g-code)">
      {children}
    </pre>
  );
}

/** Generated SQL behind a "Show SQL" toggle. */
export function ShowSql({ children }: { children: ReactNode }) {
  return (
    <details className="mt-1.5">
      <summary className="flex h-[30px] cursor-pointer items-center text-(--g-dim) hover:text-(--g-text)">
        Show SQL
      </summary>
      <SqlText>{children}</SqlText>
    </details>
  );
}

/** Type the name to confirm a production commit. */
export function ConfirmName({
  verb,
  value,
  onChange,
}: {
  verb: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const { confirmName, words } = useDataSource();
  return (
    <label className="flex flex-col gap-1.5 text-xs text-(--g-dim)">
      <span>
        Production. Type{" "}
        <b className="font-mono font-normal text-(--g-warn)">{confirmName}</b>{" "}
        to {verb}
      </span>
      <input
        className="g-field font-mono"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label={`${words.target} name`}
      />
    </label>
  );
}

/**
 * Staged changes before they're written (G4's side pane): each change, the
 * exact SQL from `renderSql`, Discard and Commit (Propose). On production
 * the commit asks for the source's confirmName.
 */
export function ReviewPanel({
  table,
  changes,
  confirmCommit,
  propose,
  actions,
  onRestore,
  onDiscard,
  onCommitted,
}: {
  /** The table's name, for the title. */
  table: string;
  changes: DataChange[];
  /** Production: type the confirmName to commit. */
  confirmCommit: boolean;
  /** The caller's level is propose: an admin approves the changeset. */
  propose: boolean;
  actions: EditActions;
  onRestore: (key: Record<string, string>) => void;
  onDiscard: () => void;
  onCommitted: () => void;
}) {
  const { confirmName } = useDataSource();
  const cli = useCli();
  const [sql, setSql] = useState<{ text?: string; error?: Error }>({});
  const [confirm, setConfirm] = useState("");
  const [committing, setCommitting] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const signature = JSON.stringify(changes);

  useEffect(() => {
    let current = true;
    setSql({});
    actions.renderSql(changes).then(
      (text) => current && setSql({ text }),
      (e: unknown) =>
        current &&
        setSql({ error: e instanceof Error ? e : new Error(String(e)) }),
    );
    return () => {
      current = false;
    };
  }, [signature, actions]);

  const tooMany = changes.length > dataLimits.changesetRows;
  const confirmed = !confirmCommit || confirm === confirmName;
  const verb = propose ? "Propose" : "Commit";

  async function commit() {
    setCommitting(true);
    setError(null);
    try {
      await actions.commit(changes, confirmCommit ? confirm : undefined);
      onCommitted();
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setCommitting(false);
    }
  }

  return (
    <aside
      aria-label="Pending changes"
      className="g-aside w-[320px] shrink-0 basis-[320px]"
    >
      <div className="flex h-7 items-center text-xs text-(--g-mut)">
        Pending changes
      </div>
      <h2 className="mt-1 text-[16px] font-semibold">
        {plural(changes.length, "change")} in {table}
      </h2>
      <p className="mb-3.5 text-xs text-(--g-mut)">
        {propose
          ? "Proposed together; an admin approves them"
          : "Committed together in one transaction"}
      </p>
      <ChangeList changes={changes} onRestore={onRestore} />
      <ShowSql>{sql.text ?? (sql.error ? "" : "Rendering SQL")}</ShowSql>
      <ErrorText error={sql.error ?? null} />
      {tooMany && (
        <p className="text-(--g-fail)">
          At most {dataLimits.changesetRows} rows per changeset. Commit in
          parts.
        </p>
      )}
      <div className="mt-auto flex flex-col gap-2.5 pt-5">
        {confirmCommit && (
          <ConfirmName
            verb={verb.toLowerCase()}
            value={confirm}
            onChange={setConfirm}
          />
        )}
        <div className="flex gap-1.5">
          <Btn className="h-8 flex-1" onClick={onDiscard}>
            Discard
          </Btn>
          <div className="grid flex-[2]">
            <Act
              primary
              className="h-8 w-full"
              cli={cli(
                `dock db apply -f changes.json${propose ? " --propose" : ""}${
                  confirmCommit ? ` --confirm ${confirmName}` : ""
                }`,
              )}
              disabled={!confirmed || tooMany || committing || !sql.text}
              onClick={commit}
            >
              {verb} {plural(changes.length, "change")}
            </Act>
          </div>
        </div>
        <ErrorText error={error} />
      </div>
    </aside>
  );
}
