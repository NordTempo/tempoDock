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
  Confirm,
  ErrorText,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import { isPending, requestQueries, useDecideRequest } from "./data";

// The access request inbox (ADR 0044): a table of requests, the selected
// one opened under its row with its exact call and arguments, what changed
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

/** The status in the list's words: "waiting", "running", "failed". */
const statusWord: Record<Status, string> = {
  pending: "waiting",
  approved: "running",
  done: "done",
  failed: "failed",
  rejected: "rejected",
  expired: "expired",
};

/** The status as read now: a pending request past its time is expired. */
const statusOf = (r: AccessRequest): Status =>
  r.status === "pending" && !isPending(r) ? "expired" : r.status;

/** An API message as a sentence: capital first letter, full stop. */
function sentence(text: string) {
  const t = text.trim();
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

/**
 * The inbox; `selected` is the request in the URL (?id=), else the oldest
 * pending one.
 */
export function RequestsPage({ selected }: { selected?: number }) {
  const requests = useSuspenseQuery(requestQueries.list()).data;
  // Without a selection, the oldest pending one is the next to decide.
  const id =
    selected ?? (requests.findLast((r) => isPending(r)) ?? requests[0])?.id;
  return (
    <>
      <PageBar title="Requests" />
      <Page>
        {requests.length === 0 ? (
          <p className="text-mut">
            No access requests. Tokens ask here when a locked environment stops
            them.
          </p>
        ) : (
          <Panel>
            {/* Fixed widths: a decision changes a row's text, not the layout. */}
            <table className="table-fixed">
              <colgroup>
                <col className="w-[38%]" />
                <col className="w-[18%]" />
                <col className="w-[20%]" />
                <col className="w-[12%]" />
                <col className="w-[12%]" />
              </colgroup>
              <thead>
                <tr>
                  <th>Request</th>
                  <th>Who</th>
                  <th>Where</th>
                  <th>Asked</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {requests.map((r) => (
                  <RequestRow key={r.id} request={r} open={r.id === id} />
                ))}
              </tbody>
            </table>
            {id !== undefined && !requests.some((r) => r.id === id) && (
              // One older than the list, opened from a link.
              <div className="border-b border-soft">
                <Detail id={id} />
              </div>
            )}
          </Panel>
        )}
      </Page>
    </>
  );
}

/** A request's row; the open one has its detail in a row under it. */
function RequestRow({
  request: r,
  open,
}: {
  request: AccessRequest;
  open: boolean;
}) {
  const org = useOrg();
  const status = statusOf(r);
  return (
    <>
      <tr className={open ? "bg-sel" : undefined}>
        <td>
          <Link
            to="/$org/requests"
            params={{ org }}
            search={{ id: r.id }}
            replace
            aria-current={open ? "true" : undefined}
            className="hover:underline"
          >
            <span className="font-mono text-mut">#{r.id}</span> {r.title}
          </Link>
        </td>
        <td>
          <Actor name={r.requested_by} kind={actorKind(r.requested_by)} />
        </td>
        <td className="font-mono">
          {r.project}/{r.environment}
        </td>
        <td
          className="text-mut"
          title={new Date(r.created_at).toLocaleString()}
        >
          {ago(r.created_at)}
        </td>
        <td className="truncate">
          <span className={statusText[status]}>{statusWord[status]}</span>
          {status === "pending" && (
            <span
              className="text-mut"
              title={`expires ${new Date(r.expires_at).toLocaleString()}`}
            >
              {" "}
              · {until(r.expires_at)} left
            </span>
          )}
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={5} className="bg-panel p-0 whitespace-normal">
            <Detail id={r.id} item={r} />
          </td>
        </tr>
      )}
    </>
  );
}

/** A request's arguments as rows, when they are a flat object. */
function argRows(args: unknown): [string, string][] | null {
  if (typeof args !== "object" || args === null || Array.isArray(args)) {
    return null;
  }
  return Object.entries(args).map(([k, v]) => [
    k.charAt(0).toUpperCase() + k.slice(1).replaceAll("_", " ").toLowerCase(),
    typeof v === "string" ? v : JSON.stringify(v),
  ]);
}

/** The service a request's arguments name, for a link to it. */
function argService(args: unknown): string | undefined {
  if (typeof args !== "object" || args === null) return undefined;
  const found = Object.entries(args).find(
    ([k, v]) => k.toLowerCase() === "service" && typeof v === "string",
  );
  return typeof found?.[1] === "string" ? found[1] : undefined;
}

/**
 * One request: what it asks, as the exact call and its arguments, then
 * the decision and, apart from it, how the run went. The list's copy
 * shows at once; the detail adds the arguments and the changes since
 * (admins). One older than the list is fetched by itself.
 */
function Detail({ id, item }: { id: number; item?: AccessRequest }) {
  const org = useOrg();
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
  const args = argRows(r.args);
  const service = argService(r.args);
  return (
    <div className="flex min-w-0 flex-col gap-3.5 px-4 py-3.5">
      <dl className="grid grid-cols-[100px_minmax(0,1fr)] gap-x-3 gap-y-1">
        {r.reason && <Row label="Reason">{r.reason}</Row>}
        <Row label="Call">
          {r.title}{" "}
          <span className="font-mono text-mut">
            {r.operation} via {r.via === "mcp" ? "MCP" : "API"}
          </span>
        </Row>
        {args?.map(([k, v]) => (
          <Row key={k} label={k}>
            <span className="font-mono">{v}</span>
          </Row>
        ))}
        <Row label="Runs as">
          <Actor name={r.requested_by} kind={actorKind(r.requested_by)} />
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
        {r.decided_by && (
          <Row label="Decision">
            {status === "rejected" ? "Rejected" : "Approved"} by {r.decided_by}
            {r.decided_at && (
              <span className="text-mut"> · {ago(r.decided_at)}</span>
            )}
          </Row>
        )}
        {r.comment && <Row label="Comment">{r.comment}</Row>}
        {(status === "approved" ||
          status === "done" ||
          status === "failed") && (
          <Row label="Run">
            <span className={statusText[status]}>
              {status === "approved"
                ? "Running"
                : status === "done"
                  ? "Done"
                  : "Failed"}
            </span>
            {r.error && (
              <span className="block whitespace-pre-wrap text-sec">
                {sentence(r.error)}
              </span>
            )}
          </Row>
        )}
        {status === "expired" && (
          <Row label="Status">
            <span className="text-mut">
              Expired without a decision. The token can ask again.
            </span>
          </Row>
        )}
      </dl>
      {args === null && r.args !== undefined && r.args !== null && (
        <pre className="overflow-auto rounded-panel border border-line bg-page px-3 py-2.5 font-mono text-2xs leading-relaxed text-sec">
          {JSON.stringify(r.args, null, 2)}
        </pre>
      )}
      {r.changes && r.changes.length > 0 && (
        <VariableDiff changes={r.changes} />
      )}
      {changes > 0 && status === "pending" && (
        <p className="text-warn-text">
          ▲ {plural(changes, "change")} in {where} since this was asked
        </p>
      )}
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
      {status === "pending" ? (
        <Decide request={r} />
      ) : (
        <p className="flex flex-wrap gap-4">
          {service ? (
            <Link
              to="/$org/$project/$environment/$service"
              params={{
                org,
                project: r.project,
                environment: r.environment,
                service,
              }}
            >
              Open {service}
            </Link>
          ) : (
            <Link
              to="/$org/$project/$environment"
              params={{ org, project: r.project, environment: r.environment }}
            >
              Open {where}
            </Link>
          )}
        </p>
      )}
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
 * Reject, or Approve and run (the primary, on the right like every
 * confirm), with an optional comment. When the pre-check says the run
 * would fail, Approve is disabled with the reason, and the service to fix
 * is a link away. Only admins in a UI session decide; tokens never can,
 * so there is no CLI equivalent.
 */
function Decide({ request: r }: { request: AccessRequest }) {
  const id = r.id;
  const session = useSession();
  const org = useOrg();
  const decide = useDecideRequest();
  const [comment, setComment] = useState("");
  const [asking, setAsking] = useState(false);
  const admin = session?.kind === "user" && session.scope === "admin";
  // The server's dry run of this request, asked when it is shown.
  const check = useQuery({ ...requestQueries.check(id), enabled: admin }).data;
  const doomed = check && !check.ok ? check : undefined;
  if (!admin) {
    return (
      <p className="text-faint">
        An admin of the organization decides in the UI.
      </p>
    );
  }
  const run = (approve: boolean) =>
    decide.mutate({ id, approve, comment: comment.trim() });
  // Approving runs the call at once in a locked environment: asked first,
  // in the one Confirm, like every other change that affects what runs.
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
      {doomed && (
        <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-warn-text">
          <span>
            <span className="text-warn">▲</span> Would fail:{" "}
            {doomed.reason ?? "the server's check refused it."}
          </span>
          {doomed.service ? (
            <Link
              to="/$org/$project/$environment/$service"
              params={{
                org,
                project: r.project,
                environment: r.environment,
                service: doomed.service,
              }}
              className="btn sm no-underline"
            >
              Open {doomed.service}
            </Link>
          ) : (
            <Link
              to="/$org/$project/$environment"
              params={{ org, project: r.project, environment: r.environment }}
              className="btn sm no-underline"
            >
              Open {r.environment}
            </Link>
          )}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 text-xs text-mut">
          Runs this once as {r.requested_by}. The lock stays on.
        </span>
        <Button small disabled={decide.isPending} onClick={() => run(false)}>
          Reject
        </Button>
        <Button
          small
          primary
          disabled={decide.isPending || !!doomed}
          reason={doomed ? "Fix the cause first, or reject." : undefined}
          onClick={() => setAsking(true)}
        >
          Approve and run…
        </Button>
      </div>
      {asking && (
        <Confirm
          title={`Run "${r.title}" in ${r.project}/${r.environment}?`}
          action="Approve and run"
          pending={decide.isPending}
          error={decide.error}
          onConfirm={() =>
            decide.mutate(
              { id, approve: true, comment: comment.trim() },
              { onSuccess: () => setAsking(false) },
            )
          }
          onCancel={() => setAsking(false)}
        >
          <p>
            It runs once now, as {r.requested_by}, and {r.environment} stays
            locked.
          </p>
        </Confirm>
      )}
      <ErrorText error={decide.error} />
    </div>
  );
}
