import type { AuditEvent } from "../../data/types";
import { clockSec, day } from "../../ui/format";
import { Actor } from "../../ui/kit";

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

/** Audit events, newest first. Token rows carry a blue edge. */
export function AuditTable({
  events,
  compact,
}: {
  events: AuditEvent[];
  /** Time, actor and a sentence; for short lists inside other pages. */
  compact?: boolean;
}) {
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
              <td className="w-full whitespace-normal">
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
        {events.map((e) => (
          <tr key={e.id}>
            <td
              className={`pl-2.5 font-mono text-mut ${
                e.actor_kind === "token"
                  ? "shadow-[inset_2px_0_0_var(--color-prog)]"
                  : ""
              }`}
            >
              {when(e.created_at)}
            </td>
            <td>
              <Actor name={e.actor} kind={e.actor_kind} />
            </td>
            <td>{actionLabel(e.action)}</td>
            <td className="font-mono">{e.target}</td>
            <td className="w-full whitespace-normal text-mut">{e.summary}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
