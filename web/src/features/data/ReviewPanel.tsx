import { type ReactNode, useEffect, useState } from "react";
import { plural } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
import type { DataChange } from "./types";
import { dataLimits } from "./types";

/** How staged edits are turned into SQL and committed (see useEditActions). */
export type EditActions = {
  /** The transaction a commit would run (POST /data/changesets, dry_run). */
  renderSql: (changes: DataChange[]) => Promise<string>;
  /**
   * Commit, or propose for callers with level propose. `confirm` is the
   * environment's name on production.
   */
  commit: (changes: DataChange[], confirm?: string) => Promise<void>;
};

/** A changeset's rows: op, key, and old → new per edited column. */
export function ChangeList({
  changes,
  onRestore,
}: {
  changes: readonly DataChange[];
  /** Unstage one row (while reviewing staged edits). */
  onRestore?: (key: Record<string, string>) => void;
}) {
  return (
    <ul className="font-mono text-xs">
      {changes.map((c, i) => {
        const key = Object.values(c.key ?? {}).join(", ");
        const values = c.op === "insert" ? c.new : c.op === "update" && c.new;
        return (
          <li
            key={`${c.table}${c.op}${key || i}`}
            className="flex gap-3 py-0.5"
          >
            <span
              className={`w-12 ${c.op === "delete" ? "text-fail" : "text-warn"}`}
            >
              {c.op}
            </span>
            <span className="w-16 truncate text-mut" title={c.table}>
              {key}
            </span>
            <span className="min-w-0 flex-1 truncate">
              {Object.entries(values || {}).map(([column, value]) => (
                <span key={column} className="mr-4">
                  {column}:{" "}
                  {c.op === "update" && (
                    <>
                      <span className="text-mut">
                        {c.old?.[column] ?? "NULL"}
                      </span>{" "}
                      →{" "}
                    </>
                  )}
                  {value ?? "NULL"}
                </span>
              ))}
            </span>
            {onRestore && c.key && (
              <button
                type="button"
                className="lnk"
                onClick={() => c.key && onRestore(c.key)}
              >
                undo
              </button>
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
    <pre className="max-h-48 overflow-auto border-l-2 border-line py-1 pl-3 font-mono text-xs whitespace-pre-wrap text-[#d4d4d4]">
      {children}
    </pre>
  );
}

/**
 * Staged changes before they're written: each change, the exact SQL from
 * `renderSql`, and Commit (Propose) or Discard. On production the commit
 * asks for the environment's name.
 */
export function ReviewPanel({
  changes,
  environment,
  confirmCommit,
  propose,
  actions,
  flags,
  onRestore,
  onDiscard,
  onCommitted,
}: {
  changes: DataChange[];
  environment: string;
  /** Production: type the environment's name to commit. */
  confirmCommit: boolean;
  /** The caller's level is propose: an admin approves the changeset. */
  propose: boolean;
  actions: EditActions;
  /** CLI flags naming the service, e.g. "-s postgres -e staging". */
  flags: string;
  onRestore: (key: Record<string, string>) => void;
  onDiscard: () => void;
  onCommitted: () => void;
}) {
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
  const confirmed = !confirmCommit || confirm === environment;
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
    <section
      aria-label="Review changes"
      className="flex flex-col gap-2 border-t border-warn-edge pt-3"
    >
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h2 className="text-md font-semibold">Review</h2>
        <span className="text-mut">
          {plural(changes.length, "change")}, one transaction
          {propose && ", an admin approves it"}
        </span>
      </div>
      <ChangeList changes={changes} onRestore={onRestore} />
      <SqlText>{sql.text ?? (sql.error ? "" : "Rendering SQL")}</SqlText>
      <ErrorText error={sql.error ?? null} />
      {tooMany && (
        <p className="text-fail">
          At most {dataLimits.changesetRows} rows per changeset. Commit in
          parts.
        </p>
      )}
      <div className="flex flex-wrap items-start gap-2.5">
        {confirmCommit && (
          <label className="flex items-center gap-2 self-center">
            <span>
              Type <b className="font-mono text-warn">{environment}</b> to{" "}
              {verb.toLowerCase()}
            </span>
            <input
              className="field w-36 py-1 font-mono"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              aria-label="Environment name"
            />
          </label>
        )}
        <Act
          primary
          cli={`dock db apply -f changes.json${propose ? " --propose" : ""}${
            confirmCommit ? ` --confirm ${environment}` : ""
          } ${flags}`}
          disabled={!confirmed || tooMany || committing || !sql.text}
          onClick={commit}
        >
          {verb} {plural(changes.length, "change")}
        </Act>
        <Button onClick={onDiscard}>Discard</Button>
        <span className="self-center">
          <ErrorText error={error} />
        </span>
      </div>
    </section>
  );
}
