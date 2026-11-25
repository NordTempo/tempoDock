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
  SectionHead,
  StateText,
} from "../../ui/kit";
import type { Tone } from "../../ui/status";
import { isPending, requestQueries, useDecideRequest } from "./data";

// Access requests (ADR 0044) in the Inbox (ADR 0076), as its "Waiting for
// you": one line per request (who asks to do what, where, until when,
// with Reject and Approve and run), opened under its line on a click
// with its exact call and arguments and what changed since. Live over
// `access_request` events.

type Status = AccessRequest["status"];

/** A status's tone: its chip, like statuses everywhere else. */
const statusTone: Record<Status, Tone> = {
  pending: "warn",
  approved: "prog",
  done: "ok",
  failed: "fail",
  rejected: "mut",
  expired: "mut",
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
 * The organization's access requests, the Inbox's first section (for
 * admins): a line per waiting request, then on request the decided ones.
 * `selected` (the URL's ?request=) is opened under its line, and stays
 * listed once decided so its result shows. Nothing renders while there
 * are no requests at all.
 */
export function AccessRequests({ selected }: { selected?: number }) {
  const requests = useSuspenseQuery(requestQueries.list()).data;
  const [decided, setDecided] = useState(false);
  const waiting = requests.filter((r) => isPending(r));
  const opened = requests.find((r) => r.id === selected);
  const shown = decided
    ? requests
    : requests.filter((r) => isPending(r) || r.id === selected);
  const rest = requests.length - shown.length;
  if (requests.length === 0 && selected === undefined) return null;
  return (
    <section aria-labelledby="requests">
      <SectionHead
        id="requests"
        aside={waiting.length > 0 && waiting.length}
        right={
          (rest > 0 || decided) && (
            <Button small onClick={() => setDecided(!decided)}>
              {decided ? "Hide decided" : `Show ${rest} decided`}
            </Button>
          )
        }
      >
        Waiting for you
      </SectionHead>
      {shown.length === 0 && selected === undefined ? (
        <p className="py-2 text-mut">
          Nothing waits for you. Tokens ask here when a locked environment stops
          them.
        </p>
      ) : (
        <ul className="border-t border-line">
          {shown.map((r) => (
            <RequestLine key={r.id} request={r} open={r.id === selected} />
          ))}
          {selected !== undefined && !opened && (
            // One older than the list, opened from a link.
            <li className="border-b border-soft bg-panel">
              <Detail id={selected} />
            </li>
          )}
        </ul>
      )}
    </section>
  );
}

/** "Restart a service" as it reads after "asks to": "restart a service". */
const lowerFirst = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);

/**
 * A request in one line: who asks to do what and where, then until when
 * (waiting) or how it ended, and for a waiting one Reject and Approve and
 * run. The words open it under the line (and close it again); the open
 * one decides inside, with a comment.
 */
function RequestLine({
  request: r,
  open,
}: {
  request: AccessRequest;
  open: boolean;
}) {
  const org = useOrg();
  const status = statusOf(r);
  const service = argService(r.args);
  return (
    <li className={`border-b border-soft ${open ? "bg-panel" : ""}`}>
      <div
        className={`flex flex-wrap items-center gap-x-3 gap-y-1.5 py-2 ${open ? "px-4" : ""}`}
      >
        <Link
          to="/$org/inbox"
          params={{ org }}
          search={open ? {} : { request: r.id }}
          replace
          aria-expanded={open}
          className="-my-[2px] min-h-[24px] min-w-0 flex-1 text-fg no-underline hover:underline"
        >
          <Actor name={r.requested_by} kind={actorKind(r.requested_by)} /> asks
          to {lowerFirst(r.title)} in{" "}
          <span className="font-mono">
            {r.project}/{r.environment}
            {service && `/${service}`}
          </span>
        </Link>
        {status === "pending" ? (
          <span
            className="text-xs text-mut"
            title={`expires ${new Date(r.expires_at).toLocaleString()}`}
          >
            asked {ago(r.created_at)} · expires in {until(r.expires_at)}
          </span>
        ) : (
          <StateText
            state={{ tone: statusTone[status], label: statusWord[status] }}
          />
        )}
        {status === "pending" && !open && <Decide request={r} compact />}
      </div>
      {open && <Detail id={r.id} item={r} />}
    </li>
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
    <div className="flex min-w-0 flex-col gap-3.5 px-4 pt-1 pb-3.5">
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
            <StateText
              state={{
                tone: statusTone[status],
                label:
                  status === "approved"
                    ? "running"
                    : status === "done"
                      ? "done"
                      : "failed",
              }}
            />
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
 * is a link away. `compact` is the request's line: the two buttons only
 * (no comment, the reason in Approve's tooltip). Only whoever decides it
 * (r.decide), in a UI session, decides; tokens never can, so there is no
 * CLI equivalent.
 */
function Decide({
  request: r,
  compact,
}: {
  request: AccessRequest;
  compact?: boolean;
}) {
  const id = r.id;
  const session = useSession();
  const org = useOrg();
  const decide = useDecideRequest();
  const [comment, setComment] = useState("");
  const [asking, setAsking] = useState(false);
  // The server says who decides: an approver there, or the person whose
  // agent asked them first.
  const admin = session?.kind === "user" && !!r.decide;
  // The server's dry run of this request, asked when it is shown.
  const check = useQuery({ ...requestQueries.check(id), enabled: admin }).data;
  const doomed = check && !check.ok ? check : undefined;
  if (!admin) {
    return compact ? null : (
      <p className="text-faint">Its approvers decide in the UI.</p>
    );
  }
  const run = (approve: boolean) =>
    decide.mutate({ id, approve, comment: comment.trim() });
  const confirm = asking && (
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
        It runs once now, as {r.requested_by}, and {r.environment} stays locked.
      </p>
    </Confirm>
  );
  if (compact) {
    return (
      <span className="flex flex-wrap items-center gap-2">
        <ErrorText error={decide.error} />
        <Button small disabled={decide.isPending} onClick={() => run(false)}>
          Reject
        </Button>
        <Button
          small
          primary
          disabled={decide.isPending || !!doomed}
          title={
            doomed
              ? `Would fail: ${doomed.reason ?? "the server's check refused it"}. Open it to see why.`
              : undefined
          }
          onClick={() => setAsking(true)}
        >
          Approve and run…
        </Button>
        {confirm}
      </span>
    );
  }
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
      {confirm}
      <ErrorText error={decide.error} />
    </div>
  );
}
