import { type ReactNode, useEffect, useState } from "react";
import { plural } from "../../ui/format";
import { useCli, useDataSource } from "./api";
import { Act, Btn, ErrorText } from "./kit";
import type { DataChange, DataChangesetRequest, DataKeyChange } from "./types";
import { dataLimits } from "./types";

/** A changeset's content: row changes, or a Redis database's key changes. */
export type ChangeBody = Pick<DataChangesetRequest, "changes" | "key_changes">;

/** How staged edits are turned into SQL and committed (see useEditActions). */
export type EditActions = {
  /** The transaction a commit would run (POST /data/changesets, dry_run). */
  renderSql: (body: ChangeBody) => Promise<string>;
  /**
   * Commit, or propose: always for callers with level propose, and on
   * request (`propose`) for writers. `confirm` is the source's confirmName
   * (an environment's or connection's name) on production; `message` says
   * why, for reviewers.
   */
  commit: (body: ChangeBody, options: CommitOptions) => Promise<void>;
};

export type CommitOptions = {
  confirm?: string;
  message?: string;
  propose?: boolean;
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

/**
 * A Redis changeset's changes: the key and the operation, then the field
 * and what it was (red) and becomes (green).
 */
export function KeyChangeList({
  changes,
  onRestore,
}: {
  changes: readonly DataKeyChange[];
  /** Unstage one change (while reviewing staged edits). */
  onRestore?: (index: number) => void;
}) {
  if (!changes.length) return null;
  return (
    <ul className="flex flex-col gap-2">
      {changes.map((c, i) => (
        // A key can change twice; the place tells the changes apart.
        <li
          key={`${i}:${c.key}:${c.op}`}
          className="overflow-hidden rounded-lg border border-(--g-line)"
        >
          <div className="flex h-[30px] items-center gap-2 bg-(--g-raise) px-2.5 text-xs text-(--g-dim)">
            <span className="truncate font-mono" title={c.key}>
              {c.key}
            </span>
            <span
              className={
                c.op === "delete" || c.op.endsWith("rem") || c.op === "hdel"
                  ? "text-(--g-fail)"
                  : "text-(--g-mut)"
              }
            >
              {c.op}
            </span>
            {onRestore && (
              <button
                type="button"
                className="ml-auto text-(--g-mut) hover:text-(--g-text)"
                onClick={() => onRestore(i)}
              >
                Undo
              </button>
            )}
          </div>
          {(c.field !== undefined ||
            c.value !== undefined ||
            c.ttl_ms !== undefined) && (
            <div className="g-diff break-all">
              {c.field !== undefined && (
                <span className="text-(--g-mut)">{c.field}</span>
              )}
              {c.old?.value !== undefined && c.op !== "lrem" && (
                <div className="g-del">− {cellText(c.old.value)}</div>
              )}
              {c.value !== undefined && (
                <div className="g-add">+ {cellText(c.value)}</div>
              )}
              {c.ttl_ms !== undefined && (
                <div className="g-add">
                  + TTL{" "}
                  {c.ttl_ms < 0 ? "none" : `${Math.round(c.ttl_ms / 1000)}s`}
                </div>
              )}
            </div>
          )}
        </li>
      ))}
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

/** Generated SQL (or Redis commands) behind a "Show SQL" toggle. */
export function ShowSql({
  label = "Show SQL",
  children,
}: {
  label?: string;
  children: ReactNode;
}) {
  return (
    <details className="mt-1.5">
      <summary className="flex h-[30px] cursor-pointer items-center text-(--g-dim) hover:text-(--g-text)">
        {label}
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
  body,
  confirmCommit,
  propose,
  actions,
  message: initialMessage = "",
  onRestore,
  onRestoreKey,
  onDiscard,
  onCommitted,
}: {
  /** The table's (key's, or changes file's) name, for the title. */
  table: string;
  body: ChangeBody;
  /** Production: type the confirmName to commit. */
  confirmCommit: boolean;
  /** The caller's level is propose: an admin approves the changeset. */
  propose: boolean;
  actions: EditActions;
  /** Why, for reviewers, to start from (a changes file's). */
  message?: string;
  /** Unstage a row change by its key, or a key change by its index. */
  onRestore?: (key: Record<string, string>) => void;
  onRestoreKey?: (index: number) => void;
  onDiscard: () => void;
  onCommitted: () => void;
}) {
  const { confirmName } = useDataSource();
  const cli = useCli();
  const [sql, setSql] = useState<{ text?: string; error?: Error }>({});
  const [confirm, setConfirm] = useState("");
  const [committing, setCommitting] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [message, setMessage] = useState(initialMessage);
  // A writer may still leave the changeset for an admin (--propose).
  const [proposeOnly, setProposeOnly] = useState(false);
  const proposing = propose || proposeOnly;
  // A proposal writes nothing: its approver confirms instead.
  const confirming = confirmCommit && !proposeOnly;
  const signature = JSON.stringify(body);
  const count = (body.changes?.length ?? 0) + (body.key_changes?.length ?? 0);
  // Redis' changes run as commands, not SQL.
  const keyed = !!body.key_changes?.length;

  useEffect(() => {
    let current = true;
    setSql({});
    actions.renderSql(body).then(
      (text) => current && setSql({ text }),
      (e: unknown) =>
        current &&
        setSql({ error: e instanceof Error ? e : new Error(String(e)) }),
    );
    return () => {
      current = false;
    };
  }, [signature, actions]);

  const tooMany = count > dataLimits.changesetRows;
  const confirmed = !confirming || confirm === confirmName;
  const verb = proposing ? "Propose" : "Commit";

  async function commit() {
    setCommitting(true);
    setError(null);
    try {
      await actions.commit(body, {
        confirm: confirming ? confirm : undefined,
        message: message.trim() || undefined,
        propose: proposeOnly,
      });
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
        {plural(count, "change")} in {table}
      </h2>
      <p className="mb-3.5 text-xs text-(--g-mut)">
        {proposing
          ? "Proposed together; an admin approves them"
          : "Committed together in one transaction"}
      </p>
      <ChangeList changes={body.changes ?? []} onRestore={onRestore} />
      <KeyChangeList
        changes={body.key_changes ?? []}
        onRestore={onRestoreKey}
      />
      <ShowSql label={keyed ? "Show commands" : "Show SQL"}>
        {sql.text ?? (sql.error ? "" : "Rendering")}
      </ShowSql>
      <ErrorText error={sql.error ?? null} />
      {tooMany && (
        <p className="text-(--g-fail)">
          At most {dataLimits.changesetRows} changes per changeset. Commit in
          parts.
        </p>
      )}
      <div className="mt-auto flex flex-col gap-2.5 pt-5">
        <label className="flex flex-col gap-1.5 text-xs text-(--g-dim)">
          Why, for reviewers
          <input
            className="g-field"
            value={message}
            maxLength={1000}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="optional"
          />
        </label>
        {!propose && (
          <label className="flex items-center gap-2 text-xs text-(--g-dim)">
            <input
              type="checkbox"
              checked={proposeOnly}
              onChange={(e) => setProposeOnly(e.target.checked)}
            />
            Only propose, for an admin to approve
          </label>
        )}
        {confirming && (
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
                `dock db apply -f changes.json${message.trim() ? ` -m ${JSON.stringify(message.trim())}` : ""}${proposing ? " --propose" : ""}${
                  confirming ? ` --confirm ${confirmName}` : ""
                }`,
              )}
              disabled={!confirmed || tooMany || committing || !sql.text}
              onClick={commit}
            >
              {verb} {plural(count, "change")}
            </Act>
          </div>
        </div>
        <ErrorText error={error} />
      </div>
    </aside>
  );
}
