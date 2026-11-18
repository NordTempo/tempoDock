import type { BackupRun, ServiceBackups } from "../../data/types";
import { clockSec, day } from "../../ui/format";
import type { Snapshot, SnapshotRestore } from "./data";

// One restore model (ADR 0061): a service's backups and snapshots are
// both restore points, restored with one action and one dialog; the list
// groups them by kind, newest first.

/** Where a restore point stands. */
export type PointState = "running" | "ready" | "failed" | "removed";

/**
 * A backup run or a snapshot, as the restore list shows it. A restore of a
 * backup is a row of the list too: its run, with operation "restore".
 */
export type RestorePoint = {
  key: string;
  at: string;
  size: number;
  state: PointState;
  /** "this server" or "off-site"; "server <name>" for a snapshot elsewhere. */
  where: string;
  /** The server's name, when known and the column says "this server". */
  server?: string;
  /** The error, a snapshot's note or why it can't be restored here. */
  detail?: string;
  by: string;
  /** "daily", "manual" or a snapshot's trigger; empty for a restore. */
  trigger: string;
} & (
  | { kind: "backup"; run: BackupRun }
  | { kind: "snapshot"; snapshot: Snapshot }
);

const backupState = (r: BackupRun): PointState =>
  r.status === "running"
    ? "running"
    : r.status === "failed"
      ? "failed"
      : r.operation === "backup" && (r.pruned || !r.snapshot)
        ? "removed"
        : "ready";

/** A point-in-time base backup (ADR 0093), not a dump. */
export const isBaseRun = (r: BackupRun) => r.kind === "pitr";

/** A restore of a backup, listed among the points it restored from. */
export const isRestoreRun = (p: RestorePoint) =>
  p.kind === "backup" && p.run.operation === "restore";

/**
 * Backups, their restores and snapshots, newest first. Base backups that
 * went well are left out: they restore through a time, and
 * the strip shows them; a running or failed one is listed, for its log.
 * server is the service's server (operators only), so a snapshot kept
 * there reads "this server" like a backup kept there.
 */
export function restorePoints(
  backups: ServiceBackups,
  snapshots: Snapshot[],
  server?: string,
): RestorePoint[] {
  const fromBackups = (backups.runs ?? [])
    .filter(
      (r) =>
        r.operation === "restore" || !isBaseRun(r) || r.status !== "succeeded",
    )
    .map((run): RestorePoint => {
      const restore = run.operation === "restore";
      return {
        kind: "backup",
        run,
        key: `b${run.id}`,
        at: run.started_at,
        size: restore ? 0 : run.size_bytes,
        state: backupState(run),
        where: restore ? "" : run.off_box ? "off-site" : "this server",
        server: restore || run.off_box ? undefined : server,
        detail: restore
          ? run.error
          : (run.error ??
            (run.pruned ? "removed by retention" : undefined) ??
            run.not_restorable),
        by: run.created_by,
        trigger: restore ? "" : run.trigger === "schedule" ? "daily" : "manual",
      };
    });
  const fromSnapshots = snapshots.map(
    (snapshot): RestorePoint => ({
      kind: "snapshot",
      snapshot,
      key: `s${snapshot.id}`,
      at: snapshot.started_at,
      size: snapshot.size_bytes,
      state: snapshot.status,
      // Only operators see the server's name; snapshots stay on the
      // service's server unless it moved since.
      where:
        !snapshot.server || snapshot.server === server
          ? "this server"
          : `server ${snapshot.server}`,
      server: snapshot.server || undefined,
      detail: snapshot.error ?? snapshot.note,
      by: snapshot.created_by,
      trigger: snapshot.trigger.replace("_", " "),
    }),
  );
  return [...fromBackups, ...fromSnapshots].sort(
    (a, b) => Date.parse(b.at) - Date.parse(a.at),
  );
}

/**
 * A finished point with nothing in it: a dump or a copy of 0 bytes (an
 * empty volume measures 0 KiB on some filesystems). It restores nothing,
 * so it is never "latest" and its dialog warns.
 */
export const isEmpty = (p: RestorePoint) =>
  p.state === "ready" && p.size <= 0 && !isRestoreRun(p);

/** What a restore run restored: "backup 9d03b7a1", or the service at a time. */
const restoredFromRun = (r: BackupRun) =>
  r.to_time
    ? `${r.service} as of ${day(r.to_time)} ${clockSec(r.to_time)}`
    : `backup ${r.snapshot?.slice(0, 8) ?? ""}`.trim();

/**
 * "backup 9d03b7a1", "base backup 412", "snapshot 2": a point by kind and
 * id. A restore reads "restore of backup 9d03b7a1 in place", or "into
 * db-restored".
 */
export function pointName(p: RestorePoint) {
  if (p.kind === "snapshot") return `snapshot ${p.snapshot.id}`;
  const r = p.run;
  if (r.operation === "restore") {
    const into =
      !r.restored_to || r.restored_to === r.service
        ? "in place"
        : `into ${r.restored_to}`;
    return `restore of ${restoredFromRun(r)} ${into}`;
  }
  return `${isBaseRun(r) ? "base backup" : "backup"} ${r.snapshot?.slice(0, 8) ?? r.id}`;
}

/** A restore that ran, from a backup or a snapshot. */
export type RestoreRecord = {
  key: string;
  from: string;
  /** The new service; unset for an in-place restore. */
  to?: string;
  status: "running" | "succeeded" | "failed";
  error?: string;
  at: string;
  /** The snapshot taken first, which undoes an in-place restore. */
  safety?: number;
  /** When the restored point was taken: later writes aren't in the copy. */
  pointAt?: string;
};

/** The latest restores of both kinds, newest first. */
export function restoreRecords(
  backups: ServiceBackups,
  restores: SnapshotRestore[],
  snapshots: Snapshot[],
): RestoreRecord[] {
  const backupAt = (snapshot?: string) =>
    (backups.runs ?? []).find(
      (r) => r.operation === "backup" && r.snapshot === snapshot,
    )?.started_at;
  const fromBackups = (backups.runs ?? [])
    .filter((r) => r.operation === "restore")
    .map(
      (r): RestoreRecord => ({
        key: `b${r.id}`,
        from: restoredFromRun(r),
        // A restore to a time in place names the database itself.
        to: r.restored_to === r.service ? undefined : r.restored_to,
        status: r.status,
        error: r.error,
        at: r.finished_at ?? r.started_at,
        safety: r.safety_snapshot,
        pointAt: r.to_time ?? backupAt(r.snapshot),
      }),
    );
  const fromSnapshots = restores.map(
    (r): RestoreRecord => ({
      key: `s${r.id}`,
      from: r.snapshot ? `snapshot ${r.snapshot}` : "a deleted snapshot",
      to: r.in_place ? undefined : r.to,
      status: r.status,
      error: r.error,
      at: r.finished_at ?? r.started_at,
      safety: r.safety,
      pointAt: snapshots.find((x) => x.id === r.snapshot)?.started_at,
    }),
  );
  return [...fromBackups, ...fromSnapshots].sort(
    (a, b) => Date.parse(b.at) - Date.parse(a.at),
  );
}

/**
 * The name a restore into a new service gets by default, `<db>-restored`,
 * or the first free `<db>-restored-2`, -3 … while that one exists.
 */
export function restoredName(service: string, taken: readonly string[]) {
  const base = `${service}-restored`;
  if (!taken.includes(base)) return base;
  for (let i = 2; ; i++) {
    if (!taken.includes(`${base}-${i}`)) return `${base}-${i}`;
  }
}

/**
 * The service a restored one came from, by its default name
 * (`db-restored`, `db-restored-2`): a service of the same template.
 */
export function restoredFrom(
  name: string,
  template: string | undefined,
  services: readonly { name: string; template?: string }[],
) {
  const origin = /^(.+)-restored(-\d+)?$/.exec(name)?.[1];
  if (!origin || !template) return undefined;
  return services.find((s) => s.name === origin && s.template === template)
    ?.name;
}
