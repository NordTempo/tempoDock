import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { type FeedItem, useFeed } from "../../data/feed";
import { useAuditLog } from "../../data/queries";
import type { AuditEvent } from "../../data/types";
import { useOrg } from "../../shell/location";
import { clockSec } from "../../ui/format";
import { Actor, Glyph, SectionHead } from "../../ui/kit";
import { pastLabel } from "../audit/AuditTable";

type Entry = { id: string; time: string; body: ReactNode };

/** Drops the project from audit targets: "shop/staging/web" → "staging/web". */
const local = (target: string, project: string) =>
  target.startsWith(`${project}/`) ? target.slice(project.length + 1) : target;

function auditEntry(e: AuditEvent, project: string): Entry {
  return {
    id: `a${e.id}`,
    time: e.created_at,
    body: (
      <>
        <Actor name={e.actor} kind={e.actor_kind} /> {pastLabel(e.action)}{" "}
        <span className="font-mono">{local(e.target, project)}</span>
      </>
    ),
  };
}

const feedGlyph = {
  progress: (
    <span className="text-prog">
      <Glyph char="◐" />
    </span>
  ),
  live: <span className="text-ok">✓</span>,
  failed: <span className="text-fail">✕</span>,
  restarting: <span className="text-fail">↻</span>,
  crashed: <span className="text-fail">✕</span>,
  unhealthy: <span className="text-fail">✕</span>,
} satisfies Record<FeedItem["kind"], ReactNode>;

function feedEntry(item: FeedItem): Entry {
  const { environment, service } = item.ref;
  return {
    id: `f${item.id}`,
    time: item.time,
    body: (
      <>
        {feedGlyph[item.kind]}{" "}
        <span className="font-mono">
          {environment}/{service}
          {item.release !== undefined && ` r${item.release}`}
        </span>{" "}
        {item.text}
      </>
    ),
  };
}

/**
 * What people, agents and the platform did in the project, newest first:
 * the audit log merged with live deploy and container events.
 */
export function Activity({ project }: { project: string }) {
  const org = useOrg();
  const audit = useAuditLog({ project });
  const feed = useFeed(project);
  const entries = [
    ...feed.map(feedEntry),
    ...audit.map((e) => auditEntry(e, project)),
  ]
    .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
    .slice(0, 12);

  return (
    <section aria-labelledby="activity" className="min-w-0">
      <SectionHead
        id="activity"
        right={
          <Link to="/$org/audit" params={{ org }} search={{ project }}>
            All activity
          </Link>
        }
      >
        Activity
      </SectionHead>
      {entries.length === 0 && <p className="text-mut">Nothing yet.</p>}
      <ol>
        {entries.map((entry) => (
          <li
            key={entry.id}
            className="flex gap-2.5 border-b border-soft px-0.5 py-[9px] last:border-0"
          >
            <span className="w-10 flex-none pt-px font-mono text-2xs text-faint">
              {clockSec(entry.time).slice(0, 5)}
            </span>
            <span className="min-w-0 flex-1 break-words">{entry.body}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}
