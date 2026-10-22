import { useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { useState } from "react";
import { ErrorText, Head, Who } from "../../../src/features/data/kit";
import { clock, day, isoDay, plural } from "../../../src/ui/format";
import type { AuditEvent } from "../api/types";
import { queries, useAudit, useAuditDetail } from "../data/queries";

// Activity (G9): the audit log (admins), newest first, a page at a time.
// Queries open to their SQL.
export const Route = createFileRoute("/_app/settings/audit")({
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (me?.role !== "admin") throw redirect({ to: "/" });
  },
  component: Activity,
});

const filters = [
  ["", "All"],
  ["data.", "Data"],
  ["data.query", "Queries"],
  ["user.", "People"],
  ["token.", "Agents"],
  ["connection.", "Connections"],
] as const;

const pageSize = 100;

function Activity() {
  const [action, setAction] = useState("");
  const [connection, setConnection] = useState("");
  const connections = useQuery(queries.connections());
  const audit = useAudit({
    action: action || undefined,
    connection: connection || undefined,
    limit: pageSize,
  });
  const events = audit.data?.pages.flat() ?? [];
  // Grouped by day, in order.
  const days: [string, AuditEvent[]][] = [];
  for (const e of events) {
    const last = days.at(-1);
    if (last?.[0] === isoDay(e.created_at)) last[1].push(e);
    else days.push([isoDay(e.created_at), [e]]);
  }

  return (
    <>
      <Head
        title="Activity"
        actions={
          <>
            <select
              className="g-field h-7"
              aria-label="Connection"
              value={connection}
              onChange={(e) => setConnection(e.target.value)}
            >
              <option value="">Every connection</option>
              {connections.data?.map((c) => (
                <option key={c.name}>{c.name}</option>
              ))}
            </select>
            <fieldset className="g-seg" aria-label="Show">
              {filters.map(([value, label]) => (
                <button
                  key={label}
                  type="button"
                  aria-pressed={action === value}
                  onClick={() => setAction(value)}
                >
                  {label}
                </button>
              ))}
            </fieldset>
          </>
        }
      >
        <span className="text-xs text-(--g-mut)">
          Every change and query, by people and agents
        </span>
      </Head>
      <main className="px-5 pt-5 pb-10">
        <div className="mx-auto flex max-w-[736px] flex-col">
          <ErrorText error={audit.error} />
          {audit.data && events.length === 0 && (
            <p className="text-(--g-mut)">Nothing yet.</p>
          )}
          {days.map(([d, list]) => (
            <section key={d} className="mb-5">
              <h2 className="g-label mb-1.5">
                {day(list[0]?.created_at ?? d)}
              </h2>
              <ul className="flex flex-col gap-0.5">
                {list.map((e) => (
                  <Event key={e.id} event={e} />
                ))}
              </ul>
            </section>
          ))}
          {audit.hasNextPage && (
            <button
              type="button"
              className="g-lnk self-start text-xs"
              disabled={audit.isFetchingNextPage}
              onClick={() => audit.fetchNextPage()}
            >
              Older
            </button>
          )}
        </div>
      </main>
    </>
  );
}

/** One line; one with a detail (a query's SQL) opens to it. */
function Event({ event: e }: { event: AuditEvent }) {
  const [open, setOpen] = useState(false);
  const line = (
    <>
      <span className="w-10 shrink-0 font-mono text-2xs text-(--g-mut)">
        {clock(e.created_at)}
      </span>
      <Who name={e.actor} kind={e.actor_kind} />
      <span className="min-w-0 flex-1 truncate text-(--g-dim)">
        {e.summary}
      </span>
      {e.connection && (
        <span className="text-2xs text-(--g-mut)">{e.connection}</span>
      )}
    </>
  );
  const row = "flex min-h-[38px] w-full items-center gap-2.5 px-2.5 text-left";
  if (e.detail_id === undefined) {
    return <li className={row}>{line}</li>;
  }
  return (
    <li
      className={`rounded-[10px] border ${open ? "border-(--g-line) bg-(--g-side)" : "border-transparent"}`}
    >
      <button
        type="button"
        className={`${row} rounded-[10px] hover:bg-(--g-hover)`}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {line}
      </button>
      {open && <Detail id={e.detail_id} />}
    </li>
  );
}

function Detail({ id }: { id: number }) {
  const detail = useAuditDetail(id);
  if (detail.error) {
    return (
      <p className="mx-2.5 mb-2.5 pl-[50px]">
        <ErrorText error={detail.error} />
      </p>
    );
  }
  const d = detail.data;
  if (!d) return null;
  return (
    <div className="mx-2.5 mb-2.5 ml-[60px] flex flex-col gap-1.5">
      <pre className="rounded-lg bg-(--g-raise) px-2.5 py-2 font-mono text-xs leading-[21px] whitespace-pre-wrap text-(--g-code)">
        {d.sql}
      </pre>
      <span className="text-xs text-(--g-mut)">
        <span className={d.outcome === "ok" ? "" : "text-(--g-fail)"}>
          {d.outcome}
        </span>{" "}
        · {plural(d.rows, "row")} · {d.duration_ms} ms
      </span>
    </div>
  );
}
