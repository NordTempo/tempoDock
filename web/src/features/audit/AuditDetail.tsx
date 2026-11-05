import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import type { AuditEvent } from "../../data/types";
import { elapsed } from "../../ui/format";
import { Actor, ErrorText } from "../../ui/kit";
import { actionLabel } from "./AuditTable";

/** "12 ms" under a second, else "4s", "1m 12s". */
const duration = (ms: number) => (ms < 1000 ? `${ms} ms` : elapsed(ms));

type Outcome = components["schemas"]["AuditDetail"]["outcome"];

const outcomeTone: Record<Outcome, string> = {
  ok: "text-ok",
  error: "text-fail",
  timeout: "text-fail",
  canceled: "text-warn",
  conflict: "text-warn",
};

/**
 * One audit event opened: every field, and for data events with a detail
 * (queries, commits) the SQL, rows, duration and outcome. Details never
 * change, so they are fetched once per event and kept outside the "audit"
 * key that live events refetch.
 */
export function AuditDetail({ event: e }: { event: AuditEvent }) {
  const id = e.detail_id;
  const detail = useQuery({
    queryKey: ["audit-detail", id],
    queryFn: () =>
      body(
        api.GET("/api/v1/audit/{id}/detail", {
          params: { path: { id: id ?? 0 } },
        }),
      ),
    enabled: id !== undefined,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const where = [e.project, e.environment].filter(Boolean).join("/");
  return (
    <dl className="props my-1 grid-cols-[120px_minmax(0,1fr)] bg-page">
      <Item label="When">
        <span className="font-mono">
          {new Date(e.created_at).toLocaleString("sv")}
        </span>
      </Item>
      <Item label="Who">
        <Actor name={e.actor} kind={e.actor_kind} />{" "}
        <span className="text-mut">{e.actor_kind}</span>
      </Item>
      <Item label="Action">
        {actionLabel(e.action) !== e.action && `${actionLabel(e.action)} `}
        <span className="font-mono text-xs text-mut">{e.action}</span>
      </Item>
      {where && (
        <Item label="Where">
          <span className="font-mono">{where}</span>
        </Item>
      )}
      <Item label="Target">
        <span className="font-mono">{e.target}</span>
      </Item>
      <Item label="Summary">{e.summary}</Item>
      {id !== undefined &&
        (detail.data ? (
          <>
            <Item label="Outcome">
              <span className={outcomeTone[detail.data.outcome]}>
                {detail.data.outcome}
              </span>
              <span className="text-mut">
                {" "}
                · {detail.data.rows} {detail.data.rows === 1 ? "row" : "rows"} ·{" "}
                {duration(detail.data.duration_ms)}
              </span>
            </Item>
            <Item label="SQL">
              <pre className="max-h-80 overflow-auto font-mono text-xs whitespace-pre-wrap">
                {detail.data.sql}
              </pre>
            </Item>
          </>
        ) : (
          <Item label="SQL">
            {detail.error ? (
              <ErrorText error={detail.error} />
            ) : (
              <span className="text-faint">loading</span>
            )}
          </Item>
        ))}
      <Item label="Event">
        <span className="font-mono text-mut">{e.id}</span>
      </Item>
    </dl>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}
