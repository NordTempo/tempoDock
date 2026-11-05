import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useSession } from "../../data/queries";
import type { AccessRequest } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, plural, until } from "../../ui/format";
import {
  Actor,
  actorKind,
  Button,
  ErrorText,
  Page,
  PageBar,
} from "../../ui/kit";
import { isPending, requestQueries, useDecideRequest } from "./data";

// The access request inbox (ADR 0044, mock "Review A"): the list beside
// the selected request, with its exact call and arguments, what changed
// since, and Approve and run or Reject. Live over `access_request` events.

type Status = AccessRequest["status"];

const statusText: Record<Status, string> = {
  pending: "text-warn",
  approved: "text-prog",
  done: "text-ok",
  failed: "text-fail",
  rejected: "text-mut",
  expired: "text-faint",
};

/** The status as read now: a pending request past its time is expired. */
const statusOf = (r: AccessRequest): Status =>
  r.status === "pending" && !isPending(r) ? "expired" : r.status;

/** "pending", "running", "done, approved by saile", "rejected by saile". */
function statusLabel(r: AccessRequest) {
  const status = statusOf(r);
  const by = r.decided_by ? ` by ${r.decided_by}` : "";
  switch (status) {
    case "approved":
      return `running, approved${by}`;
    case "done":
    case "failed":
      return `${status}, approved${by}`;
    case "rejected":
      return `rejected${by}`;
    default:
      return status;
  }
}

/**
 * The inbox; `selected` is the request in the URL (?id=), else the oldest
 * pending one.
 */
export function RequestsPage({ selected }: { selected?: number }) {
  const requests = useSuspenseQuery(requestQueries.list()).data;
  const pending = requests.filter((r) => isPending(r)).length;
  // Without a selection, the oldest pending one is the next to decide.
  const id =
    selected ?? (requests.findLast((r) => isPending(r)) ?? requests[0])?.id;
  return (
    <>
      <PageBar
        title="Requests"
        after={
          <span className="text-xs text-faint">
            {pending > 0
              ? `${plural(pending, "request")} waiting`
              : "tokens ask here when a locked environment stops them"}
          </span>
        }
      />
      <Page gap="gap-5">
        {requests.length === 0 ? (
          <p className="text-mut">No access requests yet.</p>
        ) : (
          <div className="panel grid md:min-h-[420px] md:grid-cols-[300px_minmax(0,1fr)]">
            <ul className="border-line max-md:border-b md:border-r">
              {requests.map((r) => (
                <li key={r.id}>
                  <RequestItem request={r} current={r.id === id} />
                </li>
              ))}
            </ul>
            {id !== undefined && (
              <Detail
                key={id}
                id={id}
                item={requests.find((r) => r.id === id)}
              />
            )}
          </div>
        )}
      </Page>
    </>
  );
}

function RequestItem({
  request: r,
  current,
}: {
  request: AccessRequest;
  current: boolean;
}) {
  const org = useOrg();
  return (
    <Link
      to="/$org/requests"
      params={{ org }}
      search={{ id: r.id }}
      replace
      aria-current={current ? "true" : undefined}
      className={`block border-b border-soft px-3.5 py-2 ${current ? "bg-sel" : "hover:bg-hover"}`}
    >
      <span className="block truncate">{r.summary}</span>
      <span className="block truncate text-2xs text-faint">
        {r.requested_by} · {ago(r.created_at)} ·{" "}
        <span className={statusText[statusOf(r)]}>{statusLabel(r)}</span>
      </span>
    </Link>
  );
}

/**
 * One request: what it asks, as the exact call and its arguments, and its
 * decision and outcome. The list's copy shows at once; the detail adds the
 * arguments and the changes since (admins). One older than the list is
 * fetched by itself.
 */
function Detail({ id, item }: { id: number; item?: AccessRequest }) {
  const detail = useQuery(requestQueries.one(id));
  const r = detail.data ?? item;
  if (!r) {
    return (
      <p className="px-4 py-3.5 text-mut">
        {detail.error ? (
          <ErrorText error={detail.error} />
        ) : (
          `Loading request #${id}…`
        )}
      </p>
    );
  }
  const status = statusOf(r);
  const where = `${r.project}/${r.environment}`;
  const changes = r.changes_since ?? 0;
  return (
    <div className="flex min-w-0 flex-col gap-3.5 px-4 py-3.5">
      <h2 className="text-md font-semibold">{r.summary}</h2>
      <dl className="grid grid-cols-[100px_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        {r.reason && <Row label="Reason">{r.reason}</Row>}
        <Row label="Call">
          {r.title} ·{" "}
          <span className="font-mono">
            {r.operation} ({r.via === "mcp" ? "MCP" : "API"})
          </span>
        </Row>
        <Row label="Asked">
          {ago(r.created_at)}
          {status === "pending" && (
            <span className="text-mut">
              {" "}
              · expires in {until(r.expires_at)}
            </span>
          )}
        </Row>
        <Row label="Runs as">
          <Actor name={r.requested_by} kind={actorKind(r.requested_by)} />
        </Row>
        <Row label="Status">
          <span className={statusText[status]}>{statusLabel(r)}</span>
          {r.decided_at && (
            <span className="text-mut"> · {ago(r.decided_at)}</span>
          )}
        </Row>
        {r.comment && <Row label="Comment">{r.comment}</Row>}
      </dl>
      {r.changes && r.changes.length > 0 && (
        <VariableDiff changes={r.changes} />
      )}
      {r.args !== undefined && r.args !== null && (
        <pre className="overflow-auto rounded-panel border border-line bg-page px-3 py-2.5 font-mono text-2xs leading-relaxed text-sec">
          {JSON.stringify(r.args, null, 2)}
        </pre>
      )}
      {changes > 0 && (
        <p className="text-warn-text">
          ▲ {plural(changes, "change")} in {where} since this was asked
        </p>
      )}
      {r.error && <p className="whitespace-pre-wrap text-fail">{r.error}</p>}
      {r.result !== undefined && r.result !== null && (
        <details>
          <summary className="cursor-pointer text-xs text-mut hover:text-fg">
            Result
          </summary>
          <pre className="mt-1.5 overflow-auto rounded-panel border border-line bg-page px-3 py-2.5 font-mono text-2xs leading-relaxed text-sec">
            {JSON.stringify(r.result, null, 2)}
          </pre>
        </details>
      )}
      {status === "pending" && <Decide id={r.id} />}
    </div>
  );
}

// VariableDiff is what approving a variables request would change now
// (GetRequest's changes, admins only): each key's current value against
// the requested one.
function VariableDiff({
  changes,
}: {
  changes: NonNullable<AccessRequest["changes"]>;
}) {
  return (
    <ul className="flex flex-col gap-0.5 font-mono text-2xs leading-relaxed">
      {changes.map((c) => (
        <li key={`${c.service ?? ""}:${c.key}`} className="break-all">
          <span className="text-mut">
            {c.scope === "environment" ? "env" : c.service}
          </span>{" "}
          {c.key}{" "}
          {c.action === "remove" ? (
            <span className="text-fail line-through">{c.old}</span>
          ) : (
            <>
              {c.old !== undefined && (
                <>
                  <span className="text-fail">{c.old}</span> →{" "}
                </>
              )}
              <span className="text-ok">{c.new}</span>
            </>
          )}
        </li>
      ))}
    </ul>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-faint">{label}</dt>
      <dd className="min-w-0 break-words text-sec">{children}</dd>
    </>
  );
}

/**
 * Approve and run, or Reject, with an optional comment. Only admins in a
 * UI session decide; tokens never can, so there is no CLI equivalent.
 */
function Decide({ id }: { id: number }) {
  const session = useSession();
  const decide = useDecideRequest();
  const [comment, setComment] = useState("");
  if (session?.kind !== "user" || session.scope !== "admin") {
    return (
      <p className="text-faint">
        An admin of the organization decides in the UI.
      </p>
    );
  }
  const run = (approve: boolean) =>
    decide.mutate({ id, approve, comment: comment.trim() });
  return (
    <div className="flex flex-col gap-2">
      <textarea
        aria-label="Comment (optional)"
        placeholder="Comment (optional)"
        rows={2}
        value={comment}
        onChange={(e) => setComment(e.target.value)}
        className="field resize-none"
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          small
          primary
          disabled={decide.isPending}
          onClick={() => run(true)}
        >
          Approve and run
        </Button>
        <Button
          small
          danger
          disabled={decide.isPending}
          onClick={() => run(false)}
        >
          Reject
        </Button>
        <span className="text-2xs text-faint">
          Runs once, as the token, with today's checks
        </span>
      </div>
      <ErrorText error={decide.error} />
    </div>
  );
}
