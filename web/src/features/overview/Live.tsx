import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { type FeedItem, useFeed } from "../../data/feed";
import {
  useAuditLog,
  useProjectOverview,
  useServers,
} from "../../data/queries";
import type { AuditEvent } from "../../data/types";
import { clockSec } from "../../ui/format";
import { Actor, Glyph, StateText } from "../../ui/kit";
import { serverKinds, serverState } from "../../ui/status";
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
 * The wide overview's right column: what people, agents and the platform
 * did, live, and the servers the project runs on.
 */
export function LiveColumn({ project }: { project: string }) {
  const audit = useAuditLog({ project });
  const feed = useFeed(project);
  const entries = [
    ...feed.map(feedEntry),
    ...audit.map((e) => auditEntry(e, project)),
  ]
    .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
    .slice(0, 12);

  return (
    <aside
      aria-label="Live activity"
      className="hidden flex-[1_0_340px] flex-col gap-[22px] border-l border-line px-5 pt-5 pb-8 min-[1600px]:flex"
    >
      <section aria-labelledby="live">
        <div className="mb-1.5 flex items-baseline gap-2">
          <h2 id="live" className="text-md font-semibold">
            Live
          </h2>
          <span className="text-xs text-mut">people and agents</span>
          <Link
            to="/audit"
            search={{ project }}
            className="ml-auto text-xs text-mut"
          >
            Audit log
          </Link>
        </div>
        {entries.length === 0 && <p className="text-mut">Nothing yet.</p>}
        <ol>
          {entries.map((entry) => (
            <li
              key={entry.id}
              className="border-b border-soft py-2 last:border-0"
            >
              <div className="font-mono text-2xs text-mut">
                {clockSec(entry.time)}
              </div>
              {entry.body}
            </li>
          ))}
        </ol>
      </section>
      <ProjectServers project={project} />
    </aside>
  );
}

function ProjectServers({ project }: { project: string }) {
  const { environments } = useProjectOverview(project);
  const used = new Set(environments.map((e) => e.environment.server));
  const servers = useServers().filter((s) => used.has(s.name));
  return (
    <section aria-labelledby="servers">
      <h2 id="servers" className="mb-1 text-md font-semibold">
        Servers
      </h2>
      <table>
        <tbody>
          {servers.map((s) => (
            <tr key={s.id}>
              <td className="font-mono">{s.name}</td>
              <td className="text-mut">
                {serverKinds(s)}
                {s.builder && ", builder"}
              </td>
              <td className="pr-0 text-right">
                <StateText state={serverState(s.status)} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

/** The narrow overview's stand-in for the Live column. */
export function Recent({ project }: { project: string }) {
  const audit = useAuditLog({ project }).slice(0, 5);
  return (
    <section aria-labelledby="recent" className="min-[1600px]:hidden">
      <h2 id="recent" className="text-md font-semibold">
        Recent{" "}
        <Link
          to="/audit"
          search={{ project }}
          className="text-xs font-normal text-mut"
        >
          audit log
        </Link>
      </h2>
      <div className="mt-1">
        <table>
          <tbody>
            {audit.map((e) => (
              <tr key={e.id}>
                <td className="font-mono text-mut">
                  {clockSec(e.created_at).slice(0, 5)}
                </td>
                <td>
                  <Actor name={e.actor} kind={e.actor_kind} />
                </td>
                <td className="w-full">
                  {pastLabel(e.action)}{" "}
                  <span className="font-mono">{local(e.target, project)}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
