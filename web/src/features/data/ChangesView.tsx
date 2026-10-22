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
import { ChangeList, ConfirmName, SqlText } from "./ReviewPanel";
import type { DataChangeset, DataPermission } from "./types";

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
export function ChangesView({ access }: { access: DataPermission }) {
  const list = useChangesets(true);
  const [selected, setSelected] = useState<number | null>(null);
  const all = list.data ?? [];
  const pending = all.filter((c) => c.status === "pending");
  const shown = [...pending, ...all.filter((c) => c.status !== "pending")];
  const current = shown.find((c) => c.id === selected) ?? shown[0];

  if (list.error)
    return <p className="p-5 text-(--g-fail)">{list.error.message}</p>;
  if (!list.data)
    return <p className="p-5 text-(--g-mut)">Loading changesets</p>;
  if (!shown.length) {
    return (
      <p className="p-5 text-(--g-mut)">
        No changesets yet.{" "}
        {access.approve
          ? "Proposals from people and agents show up here for you to approve."
          : "Edit a table to propose one."}
      </p>
    );
  }

  return (
    <div className="flex min-h-0 flex-1">
      <ul className="flex w-[320px] shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-(--g-line) p-2">
        {shown.slice(0, 200).map((c) => {
          const [label, tone] = statusText[c.status];
          return (
            <li key={c.id}>
              <button
                type="button"
                aria-current={c.id === current?.id}
                onClick={() => setSelected(c.id)}
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
                  {plural(c.changes?.length ?? 0, "change")} ·{" "}
                  {ago(c.created_at)}
                  {c.message && ` · ${c.message}`}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {current && (
        <Changeset
          key={current.id}
          changeset={current}
          approve={access.approve}
        />
      )}
    </div>
  );
}

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
      <Head title={`Changeset ${c.id}`}>
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
                cli={cli(`dock db changes reject ${c.id}`)}
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
