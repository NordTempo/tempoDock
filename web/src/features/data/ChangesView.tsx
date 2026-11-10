import { useState } from "react";
import { ago, plural, until } from "../../ui/format";
import {
  useChangesets,
  useCli,
  useCommitChangeset,
  useDataSource,
  useRejectChangeset,
} from "./api";
import { Act, ErrorText, Head, Who } from "./kit";
import {
  ChangeList,
  ConfirmName,
  type EditActions,
  KeyChangeList,
  ReviewPanel,
  SqlText,
} from "./ReviewPanel";
import type {
  DataChange,
  DataChangeset,
  DataKeyChange,
  DataPermission,
} from "./types";

type Status = DataChangeset["status"];

const ops = new Set(["insert", "update", "delete"]);

const isChange = (x: unknown): x is DataChange =>
  typeof x === "object" &&
  x !== null &&
  "op" in x &&
  typeof x.op === "string" &&
  ops.has(x.op) &&
  "table" in x &&
  typeof x.table === "string";

/** A key change in a file: an op and a key; the server checks the rest. */
const isKeyChange = (x: unknown): x is DataKeyChange =>
  typeof x === "object" &&
  x !== null &&
  "op" in x &&
  typeof x.op === "string" &&
  "key" in x &&
  typeof x.key === "string";

/** A changes file as it is loaded for review. */
type ChangesFile = {
  name: string;
  changes: DataChange[];
  key_changes: DataKeyChange[];
  message?: string;
};

/**
 * Reads a changes file like `dock db apply -f`: {"changes": [...],
 * "message": "..."}, {"key_changes": [...]} for Redis, or a bare array of
 * changes.
 */
function parseChangesFile(name: string, text: string): ChangesFile | Error {
  const wrong = new Error(
    `${name}: expected {"changes": [...]} or an array of changes, each with op (insert, update or delete) and table, or {"key_changes": [...]}, each with op and key`,
  );
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (e) {
    return new Error(`${name} is not JSON: ${(e as Error).message}`);
  }
  const wrapped =
    typeof parsed === "object" && parsed !== null && "changes" in parsed
      ? parsed
      : null;
  const keyed =
    typeof parsed === "object" && parsed !== null && "key_changes" in parsed
      ? parsed
      : null;
  const changes = Array.isArray(parsed) ? parsed : (wrapped?.changes ?? []);
  const keyChanges = keyed?.key_changes ?? [];
  if (
    !Array.isArray(changes) ||
    !Array.isArray(keyChanges) ||
    !(changes.length || keyChanges.length) ||
    !changes.every(isChange) ||
    !keyChanges.every(isKeyChange)
  )
    return wrong;
  const head = wrapped ?? keyed;
  const message =
    head && "message" in head && typeof head.message === "string"
      ? head.message
      : undefined;
  return { name, changes, key_changes: keyChanges, message };
}

const statusText: Record<DataChangeset["status"], [string, string]> = {
  preview: ["preview", "text-(--g-mut)"],
  pending: ["pending", "text-(--g-warn)"],
  committed: ["committed", "text-(--g-ok)"],
  rejected: ["rejected", "text-(--g-mut)"],
  expired: ["expired", "text-(--g-mut)"],
  conflict: ["conflict", "text-(--g-fail)"],
};

/**
 * The Changes tab: changesets the caller may see, pending first, and the
 * selected one with its rows and exact SQL. Admins approve (commit) or
 * reject pending ones; proposers follow what happened to theirs.
 */
export function ChangesView({
  access,
  actions,
}: {
  access: DataPermission;
  /** How changes are committed or proposed; absent: the caller can't. */
  actions?: EditActions;
}) {
  const list = useChangesets(true);
  const cli = useCli();
  const [selected, setSelected] = useState<number | null>(null);
  const [status, setStatus] = useState<Status | "">("");
  const [file, setFile] = useState<ChangesFile | null>(null);
  const [fileError, setFileError] = useState<Error | null>(null);
  const all = (list.data ?? []).filter((c) => !status || c.status === status);
  const pending = all.filter((c) => c.status === "pending");
  const shown = [...pending, ...all.filter((c) => c.status !== "pending")];
  const current = shown.find((c) => c.id === selected) ?? shown[0];

  if (list.error)
    return <p className="p-5 text-(--g-fail)">{list.error.message}</p>;
  if (!list.data)
    return <p className="p-5 text-(--g-mut)">Loading changesets</p>;

  const importFile = actions && (
    <label
      className="g-btn sm focus-within:ring-1 focus-within:ring-(--g-accent)"
      title={cli("dock db apply -f changes.json")}
    >
      Import changeset file
      <input
        type="file"
        accept=".json,application/json"
        className="sr-only"
        onChange={async (e) => {
          const picked = e.target.files?.[0];
          e.target.value = "";
          if (!picked) return;
          const read = parseChangesFile(picked.name, await picked.text());
          setFileError(read instanceof Error ? read : null);
          setFile(read instanceof Error ? null : read);
        }}
      />
    </label>
  );

  // Nothing yet: one line across the panel, no filter.
  if (!list.data.length && !file) {
    return (
      <div className="flex flex-wrap items-center gap-3 p-5">
        <p className="text-(--g-mut)">
          {access.approve
            ? "No changesets yet. Edits from Tables and agent proposals appear here."
            : "No changesets yet. Edit a table to propose one."}
        </p>
        {importFile}
        <ErrorText error={fileError} />
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1">
      <ul className="flex w-[320px] shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-(--g-line) p-2">
        <li className="flex flex-wrap items-center gap-2 px-1 pb-1.5">
          <select
            aria-label="Status"
            title={cli("dock db changes list --status STATUS")}
            className="g-field h-7"
            value={status}
            onChange={(e) =>
              setStatus(statuses.find((s) => s === e.target.value) ?? "")
            }
          >
            <option value="">every status</option>
            {statuses.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
          {importFile}
          <ErrorText error={fileError} />
        </li>
        {!shown.length && (
          <li className="px-1.5 text-(--g-mut)">
            {status ? `No ${status} changesets.` : "No changesets yet."}
          </li>
        )}
        {shown.slice(0, 200).map((c) => {
          const [label, tone] = statusText[c.status];
          return (
            <li key={c.id}>
              <button
                type="button"
                aria-current={!file && c.id === current?.id}
                onClick={() => {
                  setFile(null);
                  setSelected(c.id);
                }}
                className="g-item flex-col items-stretch gap-0.5 py-2"
              >
                <span className="flex items-center gap-2">
                  <span className="font-mono text-xs text-(--g-mut)">
                    #{c.id}
                  </span>
                  <span className="truncate text-(--g-text)">{c.author}</span>
                  <span className={`ml-auto text-xs ${tone}`}>{label}</span>
                </span>
                <span className="truncate text-xs text-(--g-mut)">
                  {plural(
                    (c.changes?.length ?? 0) + (c.key_changes?.length ?? 0),
                    "change",
                  )}{" "}
                  · {ago(c.created_at)}
                  {c.message && ` · ${c.message}`}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {file && actions ? (
        <div className="flex min-w-0 flex-1 justify-center overflow-y-auto">
          <ReviewPanel
            key={`${file.name}:${file.changes.length}:${file.key_changes.length}`}
            table={file.name}
            body={{ changes: file.changes, key_changes: file.key_changes }}
            message={file.message}
            confirmCommit={access.confirm_commit && access.level !== "propose"}
            propose={access.level === "propose"}
            actions={actions}
            onRestore={(key) => {
              const id = JSON.stringify(key);
              const changes = file.changes.filter(
                (c) => JSON.stringify(c.key) !== id,
              );
              setFile(
                changes.length || file.key_changes.length
                  ? { ...file, changes }
                  : null,
              );
            }}
            onRestoreKey={(i) => {
              const keyChanges = file.key_changes.filter((_, j) => j !== i);
              setFile(
                file.changes.length || keyChanges.length
                  ? { ...file, key_changes: keyChanges }
                  : null,
              );
            }}
            onDiscard={() => setFile(null)}
            onCommitted={() => setFile(null)}
          />
        </div>
      ) : (
        current && (
          <Changeset
            key={current.id}
            changeset={current}
            approve={access.approve}
          />
        )
      )}
    </div>
  );
}

const statuses = [
  "pending",
  "committed",
  "rejected",
  "expired",
  "conflict",
] as const satisfies readonly Status[];

/** One changeset: who, when, its rows and SQL, and the admin's decision. */
function Changeset({
  changeset: c,
  approve,
}: {
  changeset: DataChangeset;
  approve: boolean;
}) {
  const commit = useCommitChangeset();
  const reject = useRejectChangeset();
  const { confirmName: env } = useDataSource();
  const cli = useCli();
  const [confirm, setConfirm] = useState("");
  const [reason, setReason] = useState("");
  const [label, tone] = statusText[c.status];
  const confirmed = !c.confirm_commit || confirm === env;
  const busy = commit.isPending || reject.isPending;
  const decide = approve && c.status === "pending";

  return (
    <section
      aria-label={`Changeset ${c.id}`}
      className="flex min-w-0 flex-1 flex-col overflow-y-auto"
    >
      <Head
        title={`Changeset ${c.id}`}
        actions={
          <span className="font-mono text-2xs text-(--g-mut)">
            {cli(`dock db changes show ${c.id}`)}
          </span>
        }
      >
        <span className={`text-xs ${tone}`}>{label}</span>
      </Head>
      <div className="flex max-w-[736px] flex-col gap-3 px-5 py-4">
        <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-(--g-mut)">
          <Who name={c.author} kind={c.author_kind} />
          <span>{ago(c.created_at)}</span>
          {c.status === "pending" && c.expires_at && (
            <span>· expires in {until(c.expires_at)}</span>
          )}
          {c.decided_by && c.decided_at && (
            <span>
              · decided by {c.decided_by} {ago(c.decided_at)}
            </span>
          )}
        </p>
        {c.message && <p>{c.message}</p>}
        {c.reason && <p className="text-(--g-dim)">Reason: {c.reason}</p>}
        <ChangeList changes={c.changes ?? []} />
        <KeyChangeList changes={c.key_changes ?? []} />
        <SqlText>{c.sql}</SqlText>
        {c.rows_affected && (
          <p className="text-xs text-(--g-mut)">
            Rows affected per statement: {c.rows_affected.join(", ")}
          </p>
        )}
        {(c.conflicts ?? []).length > 0 && (
          <ul className="font-mono text-xs text-(--g-fail)">
            {c.conflicts?.map((x) => (
              <li key={`${x.change}${x.reason}`}>
                Change {x.change + 1}: {x.message}
              </li>
            ))}
          </ul>
        )}
        {c.status === "pending" && !approve && (
          <p className="text-(--g-mut)">
            Waiting for an admin to approve or reject.
          </p>
        )}
        {decide && (
          <div className="flex flex-col gap-2.5 pt-2">
            {c.confirm_commit && (
              <div className="max-w-[320px]">
                <ConfirmName
                  verb="commit"
                  value={confirm}
                  onChange={setConfirm}
                />
              </div>
            )}
            <div className="flex flex-wrap items-center gap-1.5">
              <Act
                primary
                cli={cli(
                  `dock db changes commit ${c.id}${c.confirm_commit ? ` --confirm ${env}` : ""}`,
                )}
                disabled={!confirmed || busy}
                onClick={() =>
                  commit.mutate({
                    id: c.id,
                    confirm: c.confirm_commit ? confirm : undefined,
                  })
                }
              >
                Approve and commit
              </Act>
              <input
                className="g-field w-56"
                placeholder="Reason (optional)"
                aria-label="Reject reason"
                maxLength={1000}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
              <Act
                cli={cli(
                  `dock db changes reject ${c.id}${reason.trim() ? ` --reason ${JSON.stringify(reason.trim())}` : ""}`,
                )}
                disabled={busy}
                onClick={() =>
                  reject.mutate({
                    id: c.id,
                    reason: reason.trim() || undefined,
                  })
                }
              >
                Reject
              </Act>
            </div>
          </div>
        )}
        <ErrorText error={commit.error ?? reject.error} />
      </div>
    </section>
  );
}
