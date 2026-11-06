import { Fragment, useState } from "react";
import type { AuditEvent } from "../../data/types";
import { clockSec, day } from "../../ui/format";
import { Actor, Panel, Scroll } from "../../ui/kit";
import { AuditDetail } from "./AuditDetail";

/**
 * Each audit action in plain words: the label for the log's Action column
 * ("lock environment") and the past tense that reads before the event's
 * target in sentences ("locked" blog/production). Targets often carry
 * their noun ("token deploy-bot", "request #1"), so the past tense leaves
 * it out. Actions not listed show their raw key.
 */
const words: Record<string, readonly [label: string, past: string]> = {
  "alert.channel_add": ["add alert channel", "added"],
  "alert.channel_remove": ["remove alert channel", "removed"],
  "alert.resolve": ["resolve alert", "resolved"],
  "analytics.defaults": ["change analytics defaults", "changed"],
  "analytics.disable": ["turn off analytics", "turned off analytics for"],
  "analytics.enable": ["turn on analytics", "turned on analytics for"],
  "analytics.funnel_add": ["add funnel", "added a funnel to"],
  "analytics.funnel_remove": ["remove funnel", "removed a funnel from"],
  "analytics.goal_add": ["add goal", "added a goal to"],
  "analytics.goal_remove": ["remove goal", "removed a goal from"],
  "analytics.move": ["move analytics store", "started moving analytics to"],
  "analytics.moved": ["analytics store moved", "moved analytics to"],
  "analytics.move_failed": [
    "analytics move failed",
    "failed to move analytics to",
  ],
  "analytics.replay_view": ["watch replay", "watched a replay of"],
  "analytics.server": ["place analytics store", "put the analytics store on"],
  "analytics.site": ["change site", "changed"],
  "backup.configure": ["configure backups", "configured"],
  "backup.failed": ["backup failed", "failed to back up"],
  "backup.move": ["move backups", "started moving"],
  "backup.move_copied": ["copy backups", "copied"],
  "backup.move_failed": ["backup move failed", "failed to move"],
  "backup.moved": ["backups moved", "moved"],
  "backup.restore": ["start restore", "started restoring"],
  "backup.restore_failed": ["restore failed", "failed to restore"],
  "backup.restored": ["restore done", "restored"],
  "backup.run": ["start backup", "started a backup of"],
  "backup.schedule": [
    "change backup schedule",
    "changed the backup schedule of",
  ],
  "backup.succeeded": ["backup done", "backed up"],
  "changes.apply": ["apply changes", "applied changes in"],
  "changes.discard": ["discard changes", "discarded changes in"],
  "changes.restage": ["restage changes", "restaged changes for"],
  "changes.stage_file": ["stage project file", "staged a project file in"],
  "cloudflare.connection_add": [
    "add Cloudflare connection",
    "added Cloudflare connection",
  ],
  "cloudflare.connection_remove": [
    "remove Cloudflare connection",
    "removed Cloudflare connection",
  ],
  "cloudflare.connection_update": [
    "update Cloudflare connection",
    "updated Cloudflare connection",
  ],
  "data.approve": ["approve data changes", "approved data changes to"],
  "data.browse": ["browse data", "browsed"],
  "data.commit": ["commit data changes", "committed data changes to"],
  "data.connect": ["open tunnel", "opened a tunnel to"],
  "data.disconnect": ["close tunnel", "closed a tunnel to"],
  "data.policy": ["change data policy", "changed the data policy of"],
  "data.propose": ["propose data changes", "proposed data changes to"],
  "data.query": ["run SQL", "ran SQL on"],
  "data.reject": ["reject data changes", "rejected data changes to"],
  "data.reveal": ["reveal data", "revealed data in"],
  "data.setup": ["set up data access", "set up data access for"],
  "domain.add": ["add domain", "added a domain to"],
  "domain.lost": ["lose domain", "lost a domain of"],
  "domain.remove": ["remove domain", "removed a domain from"],
  "domain.verify": ["verify domain", "verified a domain of"],
  "environment.clone": ["clone environment", "cloned an environment into"],
  "environment.create": ["create environment", "created"],
  "environment.lock": ["lock environment", "locked"],
  "environment.move": ["move environment", "started moving"],
  "environment.move_failed": ["environment move failed", "failed to move"],
  "environment.moved": ["environment moved", "moved"],
  "environment.unlock": ["unlock environment", "unlocked"],
  "git.connect": ["connect repository", "connected a repository to"],
  "git.deploy": ["queue deploy from git", "queued a deploy of"],
  "git.disconnect": ["disconnect repository", "disconnected the repository of"],
  "github.app_create": ["create GitHub App", "created GitHub App"],
  "github.app_remove": ["remove GitHub App", "removed GitHub App"],
  "github.install_assign": [
    "connect GitHub account",
    "connected GitHub account",
  ],
  "github.install_unassign": [
    "disconnect GitHub account",
    "disconnected GitHub account",
  ],
  "job.disable": ["disable job", "disabled job"],
  "job.enable": ["enable job", "enabled job"],
  "job.remove": ["remove job", "removed job"],
  "job.run": ["run job now", "ran job"],
  "job.set": ["set job", "set job"],
  "job.sync": ["sync dock.toml jobs", "synced dock.toml jobs for"],
  "log_drain.add": ["add log drain", "added"],
  "log_drain.remove": ["remove log drain", "removed"],
  "org.create": ["create organization", "created organization"],
  "org.delete": ["delete organization", "deleted organization"],
  "org.invite": ["invite member", "invited"],
  "org.invite_revoke": ["revoke invite", "revoked the invite of"],
  "org.join": ["join organization", "joined the organization as"],
  "org.leave": ["leave organization", "left the organization as"],
  "org.lock_settings": ["change lock settings", "changed the lock settings of"],
  "org.member_remove": ["remove member", "removed"],
  "org.member_role": ["change role", "changed the role of"],
  "org.registry_credential_remove": [
    "remove registry login",
    "removed the registry login for",
  ],
  "org.registry_credential_set": [
    "set registry login",
    "set the registry login for",
  ],
  "org.rename": ["rename organization", "renamed the organization to"],
  "org.require_two_factor": [
    "change two-factor rule",
    "changed the two-factor rule of",
  ],
  "org.variable_set": ["set org variable", "set org variable"],
  "org.variable_unset": ["remove org variable", "removed org variable"],
  "org.variables_reveal": [
    "reveal org variables",
    "revealed the org variables of",
  ],
  "platform.address_removed": ["remove address", "removed the address"],
  "platform.address_set": ["set address", "set the address to"],
  "platform.operators_two_factor": [
    "change operator sign-in",
    "changed the operator",
  ],
  "platform.update_failed": ["update failed", "failed to update"],
  "platform.update_started": ["start update", "started updating"],
  "platform.updated": ["update done", "updated"],
  "preview.close": ["close preview", "closed the preview"],
  "preview.create": ["open preview", "opened the preview"],
  "preview.update": ["update preview", "updated the preview"],
  "previews.disable": ["turn off previews", "turned off previews for"],
  "project.archive": ["archive project", "archived project"],
  "project.create": ["create project", "created project"],
  "project.pin": ["pin project", "pinned the environments of"],
  "project.rename": ["rename project", "renamed a project to"],
  "reconcile.drift": ["fix drift", "fixed drift in"],
  "registry.migrate": ["move registry", "started moving the"],
  "registry.migrate_failed": ["registry move failed", "failed to move the"],
  "registry.migrated": ["registry moved", "moved the"],
  "registry.prune": ["prune registry", "started pruning the"],
  "registry.prune_failed": ["registry prune failed", "failed to prune the"],
  "registry.pruned": ["registry pruned", "pruned the"],
  "registry.storage": ["change registry storage", "changed the storage of the"],
  "release.deploy": ["deploy", "deployed"],
  "release.promote": ["promote", "promoted to"],
  "release.restart": ["restart", "restarted"],
  "release.rollback": ["roll back", "rolled back"],
  "request.approve": ["approve request", "approved"],
  "request.create": ["request access", "opened"],
  "request.reject": ["reject request", "rejected"],
  "request.replace": ["replace request", "replaced an older request with"],
  "server.add": ["add server", "added"],
  "server.join": ["server joined", "joined"],
  "server.prune": ["prune server", "pruned"],
  "server.remove": ["remove server", "removed"],
  "server.revoke": ["revoke server", "revoked"],
  "server.rotate_key": ["rotate server key", "rotated the key of"],
  "server.tunnel_adopt": ["adopt tunnel", "adopted the tunnel of"],
  "server.tunnel_create": ["create tunnel", "created a tunnel for"],
  "server.update": ["update server", "updated"],
  "server.update_failed": ["server update failed", "failed to update"],
  "server.updated": ["server updated", "updated"],
  "service.access": ["change access", "changed who can open"],
  "service.archive": ["archive service", "archived"],
  "service.create": ["create service", "created"],
  "service.database": ["set database", "set the database of"],
  "service.health_restart": ["restart unhealthy", "restarted unhealthy"],
  "service.run": ["run command", "ran a command in"],
  "service.start": ["start service", "started"],
  "service.stop": ["stop service", "stopped"],
  "service.switch_references": [
    "switch references",
    "pointed variable references at",
  ],
  "service.update": ["stage settings", "staged settings for"],
  "snapshot.create": ["take snapshot", "took a snapshot of"],
  "snapshot.created": ["snapshot taken", "took a snapshot of"],
  "snapshot.delete": ["delete snapshot", "deleted a snapshot of"],
  "snapshot.failed": ["snapshot failed", "failed to snapshot"],
  "snapshot.pruned": ["prune snapshots", "pruned snapshots of"],
  "snapshot.restore": ["restore snapshot", "started restoring a snapshot of"],
  "snapshot.restore_failed": [
    "snapshot restore failed",
    "failed to restore a snapshot of",
  ],
  "snapshot.restored": ["snapshot restored", "restored a snapshot of"],
  "storage.create": ["add storage", "added storage"],
  "storage.delete": ["remove storage", "removed storage"],
  "storage.update": ["update storage", "updated storage"],
  "token.cli_login": ["approve dock login", "approved a dock login as"],
  "token.cli_login_denied": ["deny dock login", "denied a dock login as"],
  "token.create": ["create token", "created"],
  "token.revoke": ["revoke token", "revoked"],
  "uptime.add": ["add uptime check", "added an uptime check to"],
  "uptime.remove": ["remove uptime check", "removed an uptime check from"],
  "uptime.update": ["change uptime check", "changed an uptime check of"],
  "user.2fa_add": ["add second factor", "added a second factor for"],
  "user.2fa_remove": ["remove second factor", "removed a second factor of"],
  "user.2fa_reset": ["reset two-factor", "reset the two-factor sign-in of"],
  "user.bootstrap": ["create first user", "created the first user"],
  "user.create": ["create user", "created user"],
  "user.delete": ["delete user", "deleted user"],
  "user.disable": ["disable user", "disabled user"],
  "user.enable": ["enable user", "enabled user"],
  "user.name": ["change name", "changed the name of"],
  "user.login": ["sign in", "signed in as"],
  "user.login_failed": ["failed sign-in", "failed to sign in as"],
  "user.password": ["change password", "changed the password of"],
  "user.password_failed": [
    "failed password change",
    "failed to change the password of",
  ],
  "user.reauth_failed": ["failed confirmation", "failed to confirm as"],
  "user.recovery_code_used": ["use recovery code", "used a recovery code as"],
  "user.recovery_codes": ["new recovery codes", "made new recovery codes for"],
  "user.reset_password": ["reset password", "reset the password of"],
  "user.update": ["update user", "updated user"],
  "variables.set": ["stage variables", "staged variables for"],
  "volume.add": ["stage volume", "staged a volume for"],
};

/** "lock environment" for environment.lock; the raw key if unknown. */
export const actionLabel = (action: string) => words[action]?.[0] ?? action;

/** "locked" for environment.lock, to put before its target; raw if unknown. */
export const pastLabel = (action: string) => words[action]?.[1] ?? action;

/**
 * An action in the log's words, or its raw key in faint mono when the UI
 * doesn't know it yet. `past` picks the sentence form.
 */
export function ActionText({
  action,
  past,
}: {
  action: string;
  past?: boolean;
}) {
  const w = words[action];
  if (!w) return <span className="font-mono text-xs text-faint">{action}</span>;
  return <>{past ? w[1] : w[0]}</>;
}

/** Today shows the time, older rows the date too. */
export function when(iso: string) {
  return new Date(iso).toDateString() === new Date().toDateString()
    ? clockSec(iso)
    : `${day(iso)} ${clockSec(iso).slice(0, 5)}`;
}

/** "Today", "Yesterday", "Nov 4", or "Nov 4, 2025" in another year. */
function dayHead(iso: string) {
  const d = new Date(iso);
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === yesterday.toDateString()) return "Yesterday";
  return d.getFullYear() === today.getFullYear()
    ? day(iso)
    : d.toLocaleDateString("en", {
        month: "short",
        day: "numeric",
        year: "numeric",
      });
}

/**
 * Audit events, newest first, under a line for each day. A row's time
 * opens the event: every field with the raw action key, and the SQL of
 * data events.
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
      <Scroll>
        <table>
          <tbody>
            {events.map((e) => (
              <tr key={e.id}>
                <td
                  className="font-mono text-mut"
                  title={new Date(e.created_at).toLocaleString()}
                >
                  {when(e.created_at)}
                </td>
                <td>
                  <Actor name={e.actor} kind={e.actor_kind} />
                </td>
                <td className="whitespace-normal wrap-anywhere">
                  <ActionText action={e.action} past />{" "}
                  <span className="font-mono">{e.target}</span>{" "}
                  <span className="text-mut">{e.summary}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Scroll>
    );
  }
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th className="w-[104px]">Time</th>
            <th>Actor</th>
            <th>Action</th>
            <th>Target</th>
            <th>Detail</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e, i) => {
            const opened = open === e.id;
            const head = dayHead(e.created_at);
            const prev = events[i - 1];
            const newDay = !prev || dayHead(prev.created_at) !== head;
            return (
              <Fragment key={e.id}>
                {newDay && (
                  <tr>
                    <th
                      colSpan={5}
                      scope="colgroup"
                      className="pt-4 text-xs font-semibold text-sec"
                    >
                      {head}
                    </th>
                  </tr>
                )}
                <tr className={opened ? "bg-sel" : undefined}>
                  <td className="font-mono text-mut">
                    <button
                      type="button"
                      aria-expanded={opened}
                      title={new Date(e.created_at).toLocaleString()}
                      className="-my-[2px] min-h-[24px] hover:text-fg"
                      onClick={() => setOpen(opened ? null : e.id)}
                    >
                      <span aria-hidden className="text-faint">
                        {opened ? "▾" : "▸"}
                      </span>{" "}
                      {clockSec(e.created_at)}
                    </button>
                  </td>
                  <td>
                    <Actor name={e.actor} kind={e.actor_kind} />
                  </td>
                  <td>
                    <ActionText action={e.action} />
                  </td>
                  <td className="font-mono">{e.target}</td>
                  <td className="whitespace-normal wrap-anywhere text-mut">
                    {e.summary}
                  </td>
                </tr>
                {opened && (
                  <tr>
                    <td colSpan={5} className="pb-3 whitespace-normal">
                      <AuditDetail event={e} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </Panel>
  );
}
