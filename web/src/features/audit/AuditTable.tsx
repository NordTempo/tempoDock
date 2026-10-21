import type { AuditEvent } from "../../data/types";
import { clock, day } from "../../ui/format";
import { actorDetail } from "../../ui/status";

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
              {new Date(event.at).toDateString() === today
                ? clock(event.at)
                : `${day(event.at)} ${clock(event.at)}`}
            </td>
            <td className="w-[220px]">{event.actor.name}</td>
            <td className="w-full">
              <Summary text={event.summary} subject={event.subject} />
            </td>
            <td className="text-dim">{actorDetail(event.actor)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Emphasizes the first whole-word mention of the subject. */
function Summary({ text, subject }: { text: string; subject: string | null }) {
  const at = subject ? ` ${text} `.indexOf(` ${subject} `) : -1;
  if (!subject || at < 0) return text;
  return (
    <>
      {text.slice(0, at)}
      <b className="font-semibold">{subject}</b>
      {text.slice(at + subject.length)}
    </>
  );
}
