import { Fragment, useState } from "react";
import type { AuditEvent } from "../../data/types";
import { clockSec, day } from "../../ui/format";
import { Actor } from "../../ui/kit";
import { AuditDetail } from "./AuditDetail";

/** Audit actions in a few plain words, past tense. */
const verbs: Record<string, string> = {
  "changes.apply": "apply changes",
  "changes.discard": "discard changes",
  "domain.add": "add domain",
  "domain.remove": "remove domain",
  "environment.create": "create environment",
  "project.create": "create project",
  "project.archive": "archive project",
  "release.deploy": "deploy",
  "release.restart": "restart",
  "release.rollback": "roll back",
  "service.archive": "archive service",
  "service.create": "create service",
  "service.update": "stage settings",
  "token.create": "create token",
  "token.revoke": "revoke token",
  "user.create": "create user",
  "user.delete": "delete user",
  "user.disable": "disable user",
  "user.enable": "enable user",
  "user.login": "log in",
  "user.login_failed": "failed login",
  "user.password": "change password",
  "user.password_failed": "failed password change",
  "user.bootstrap": "create first user",
  "variables.set": "stage variables",
  "volume.add": "stage volume",
  "reconcile.drift": "fix drift",
  "backup.configure": "configure backups",
  "backup.run": "start backup",
  "backup.succeeded": "backup succeeded",
  "backup.failed": "backup failed",
  "backup.schedule": "change backup schedule",
  "backup.restore": "start restore",
  "backup.restored": "restore succeeded",
  "backup.restore_failed": "restore failed",
  "data.query": "run SQL",
  "data.browse": "browse data",
  "data.commit": "commit data changes",
  "data.propose": "propose data changes",
  "data.approve": "approve data changes",
  "data.reject": "reject data changes",
  "data.reveal": "reveal data",
  "data.connect": "open tunnel",
  "data.disconnect": "close tunnel",
  "data.setup": "set up data access",
  "data.policy": "change data policy",
};

export const actionLabel = (action: string) => verbs[action] ?? action;

/** Past tense for sentences: "deployed", "staged variables in". */
const pastVerbs: Record<string, string> = {
  "changes.apply": "applied changes in",
  "changes.discard": "discarded changes in",
  "domain.add": "added",
  "domain.remove": "removed",
  "release.deploy": "deployed",
  "release.restart": "restarted",
  "release.rollback": "rolled back",
  "service.update": "staged settings for",
  "variables.set": "staged variables for",
  "volume.add": "staged a volume for",
  "service.create": "created",
  "service.archive": "archived",
  "environment.create": "created",
};

export const pastLabel = (action: string) =>
  pastVerbs[action] ?? actionLabel(action);

/** Today shows the time, older rows the date too. */
export function when(iso: string) {
  return new Date(iso).toDateString() === new Date().toDateString()
    ? clockSec(iso)
    : `${day(iso)} ${clockSec(iso).slice(0, 5)}`;
}

/**
 * Audit events, newest first. Token rows carry a blue edge. A row's time
 * opens the event: every field, and the SQL of data events.
 */
export function AuditTable({
  events,
  compact,
}: {
  events: AuditEvent[];
  /** Time, actor and a sentence; for short lists inside other pages. */
  compact?: boolean;
}) {
  const [open, setOpen] = useState<number | null>(null);
  if (events.length === 0) return <p className="py-2 text-mut">No events.</p>;
  if (compact) {
    return (
      <table>
        <tbody>
          {events.map((e) => (
            <tr key={e.id}>
              <td className="font-mono text-mut">{when(e.created_at)}</td>
              <td>
                <Actor name={e.actor} kind={e.actor_kind} />
              </td>
              <td className="whitespace-normal wrap-anywhere">
                {pastLabel(e.action)}{" "}
                <span className="font-mono">{e.target}</span>{" "}
                <span className="text-mut">{e.summary}</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    );
  }
  return (
    <table>
      <thead>
        <tr>
          <th className="pl-2.5">Time</th>
          <th>Actor</th>
          <th>Action</th>
          <th>Target</th>
          <th>Detail</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e) => {
          const opened = open === e.id;
          return (
            <Fragment key={e.id}>
              <tr className={opened ? "bg-sel" : undefined}>
                <td
                  className={`pl-2.5 font-mono text-mut ${
                    e.actor_kind === "token"
                      ? "shadow-[inset_2px_0_0_var(--color-prog)]"
                      : ""
                  }`}
                >
                  <button
                    type="button"
                    aria-expanded={opened}
                    title={opened ? "Close the event" : "Open the event"}
                    className="hover:text-fg"
                    onClick={() => setOpen(opened ? null : e.id)}
                  >
                    <span aria-hidden className="text-faint">
                      {opened ? "▾" : "▸"}
                    </span>{" "}
                    {when(e.created_at)}
                  </button>
                </td>
                <td>
                  <Actor name={e.actor} kind={e.actor_kind} />
                </td>
                <td>{actionLabel(e.action)}</td>
                <td className="font-mono">{e.target}</td>
                <td className="whitespace-normal wrap-anywhere text-mut">
                  {e.summary}
                </td>
              </tr>
              {opened && (
                <tr>
                  <td colSpan={5} className="pb-3 pl-2.5 whitespace-normal">
                    <AuditDetail event={e} />
                  </td>
                </tr>
              )}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}
