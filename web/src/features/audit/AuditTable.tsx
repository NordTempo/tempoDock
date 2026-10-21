import type { AuditEvent } from "../../data/types";
import { clock, day } from "../../ui/format";

/** Audit events, newest first. Today shows the time, older rows the date. */
export function AuditTable({ events }: { events: AuditEvent[] }) {
  if (events.length === 0) return <p className="py-2 text-dim">No events.</p>;
  const today = new Date().toDateString();
  return (
    <table className="tbl">
      <tbody>
        {events.map((event) => (
          <tr key={event.id}>
            <td className="w-[90px] font-mono text-xs text-dim">
              {new Date(event.created_at).toDateString() === today
                ? clock(event.created_at)
                : `${day(event.created_at)} ${clock(event.created_at)}`}
            </td>
            <td className="w-[220px]">{event.actor}</td>
            <td className="w-full whitespace-normal">
              <b className="font-semibold">{event.target}</b> {event.summary}
            </td>
            <td className="text-dim">{event.actor_kind}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
