import { useState } from "react";
import { ago, plural, until } from "../../ui/format";
import { Act, Actor, ErrorText } from "../../ui/kit";
import {
  useChangesets,
  useCli,
  useCommitChangeset,
  useDataSource,
  useRejectChangeset,
} from "./api";
import { ChangeList, SqlText } from "./ReviewPanel";
import type { DataChangeset, DataPermission } from "./types";

const statusText: Record<DataChangeset["status"], [string, string]> = {
  preview: ["", "text-mut"],
  pending: ["▲ pending", "text-warn"],
  committed: ["✓ committed", "text-ok"],
  rejected: ["– rejected", "text-mut"],
  expired: ["– expired", "text-mut"],
  conflict: ["✕ conflict", "text-fail"],
};

/**
 * The Changes tab: changesets the caller may see, pending first, each with
 * its rows and exact SQL. Admins approve (commit) or reject pending ones;
 * proposers follow what happened to theirs.
 */
export function ChangesView({ access }: { access: DataPermission }) {
  const list = useChangesets(true);
  const [selected, setSelected] = useState<number | null>(null);
  const all = list.data ?? [];
  const pending = all.filter((c) => c.status === "pending");
  const shown = [...pending, ...all.filter((c) => c.status !== "pending")];
  const current = shown.find((c) => c.id === selected) ?? shown[0];

  if (list.error) return <p className="text-fail">{list.error.message}</p>;
  if (!list.data) return <p className="text-mut">Loading changesets</p>;
  if (!shown.length) {
    return (
      <p className="py-2 text-mut">
        No changesets yet.{" "}
        {access.approve
          ? "Proposals from people and tokens with level propose show up here for you to approve."
          : "Edit a table to propose one."}
      </p>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 gap-5">
      <ul className="flex w-[300px] shrink-0 flex-col overflow-y-auto text-xs">
        {shown.slice(0, 200).map((c) => {
          const [label, tone] = statusText[c.status];
          return (
            <li key={c.id}>
              <button
                type="button"
                aria-current={c.id === current?.id}
                onClick={() => setSelected(c.id)}
                className={`flex w-full flex-col py-1.5 text-left hover:text-white ${
                  c.id === current?.id ? "text-white" : "text-[#d4d4d4]"
                }`}
              >
                <span className="flex gap-2">
                  <span className="font-mono">#{c.id}</span>
                  <span className={tone}>{label}</span>
                  <span className="ml-auto text-mut">{ago(c.created_at)}</span>
                </span>
                <span className="truncate text-mut">
                  {c.author} · {plural(c.changes?.length ?? 0, "change")}
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
  const { confirmName: env, words } = useDataSource();
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
      className="flex min-w-0 flex-1 flex-col gap-2.5 overflow-y-auto"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-md font-semibold">Changeset {c.id}</h2>
        <span className={tone}>{label}</span>
        <span className="text-mut">
          by <Actor name={c.author} kind={c.author_kind} />, {ago(c.created_at)}
          {c.status === "pending" &&
            c.expires_at &&
            `, expires in ${until(c.expires_at)}`}
          {c.decided_by &&
            c.decided_at &&
            `, decided by ${c.decided_by} ${ago(c.decided_at)}`}
        </span>
      </div>
      {c.message && <p>{c.message}</p>}
      {c.reason && <p className="text-mut">Reason: {c.reason}</p>}
      <ChangeList changes={c.changes ?? []} />
      <SqlText>{c.sql}</SqlText>
      {c.rows_affected && (
        <p className="font-mono text-xs text-mut">
          rows affected per statement: {c.rows_affected.join(", ")}
        </p>
      )}
      {(c.conflicts ?? []).length > 0 && (
        <ul className="font-mono text-xs text-fail">
          {c.conflicts?.map((x) => (
            <li key={`${x.change}${x.reason}`}>
              ✕ change {x.change + 1}: {x.message}
            </li>
          ))}
        </ul>
      )}
      {c.status === "pending" && !approve && (
        <p className="text-mut">Waiting for an admin to approve or reject.</p>
      )}
      {decide && (
        <div className="flex flex-wrap items-start gap-2.5">
          {c.confirm_commit && (
            <label className="flex items-center gap-2 self-center">
              <span>
                Type <b className="font-mono text-warn">{env}</b> to commit
              </span>
              <input
                className="field w-36 py-1 font-mono"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                aria-label={`${words.target} name`}
              />
            </label>
          )}
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
            className="field w-56 py-1"
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
              reject.mutate({ id: c.id, reason: reason.trim() || undefined })
            }
          >
            Reject
          </Act>
        </div>
      )}
      <ErrorText error={commit.error ?? reject.error} />
    </section>
  );
}
