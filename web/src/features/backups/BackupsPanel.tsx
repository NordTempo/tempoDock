import { Link, useLocation } from "@tanstack/react-router";
import { lazy, type ReactNode, Suspense, useEffect, useState } from "react";
import {
  useBackupSchedule,
  useRestoreBackup,
  useRunBackup,
} from "../../data/mutations";
import {
  useBackups,
  useCanDeploy,
  useEnvironment,
  useIsOperator,
} from "../../data/queries";
import type { ServiceBackups, ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, clock, day, plural, size, until } from "../../ui/format";
import {
  Act,
  actorLabel,
  Button,
  Check,
  Confirm,
  ErrorText,
  MoreItem,
  MoreMenu,
  MoreSeparator,
  Scroll,
  StateText,
} from "../../ui/kit";
import { SettingsSection } from "../../ui/sections";
import type { State } from "../../ui/status";
import { Saved } from "../create/form";
import { backupWhat, hourLabel } from "./backups";
import { CheckedLine } from "./CheckedLine";
import {
  useDeleteSnapshot,
  useRestoreSnapshot,
  useSnapshots,
  useTakeSnapshot,
} from "./data";
import type { PitrMode } from "./PitrDialog";
import { PitrRow, PitrSection } from "./PitrSection";
import { stripMarks, windowOf } from "./pitr";
import {
  isBaseRun,
  isEmpty,
  isRestoreRun,
  type PointState,
  pointName,
  type RestorePoint,
  type RestoreRecord,
  restorePoints,
  restoreRecords,
} from "./points";

// The dialogs load when one opens.
const RestoreDialog = lazy(() => import("./RestoreDialog"));
const SwitchDialog = lazy(() => import("./SwitchDialog"));
const LogDialog = lazy(() => import("./LogDialog"));
const TimeRestoreDialog = lazy(() => import("./TimeRestoreDialog"));
const PitrDialog = lazy(() => import("./PitrDialog"));

/** What the tab has open: one dialog at a time. */
type Open =
  | { what: "restore" | "log" | "delete"; point: RestorePoint }
  | { what: "switch"; to: string; since?: string }
  /** A restore to a time, at the moment picked on the strip. */
  | { what: "time"; at?: number }
  | { what: "pitr"; mode: PitrMode };

/** What one of the service's backups is, in the list's Kind column. */
const backupKind: Record<ServiceBackups["kind"], string> = {
  postgres: "dump",
  mysql: "dump",
  redis: "dump",
  mongodb: "dump",
  volume: "file backup",
};

/**
 * Settings' Backups section for a stateful service (ADR 0061, ADR 0099):
 * first how its backups stand (schedule and the last good one, where they
 * are kept with the way to fix "this server only", the weekly check that
 * they restore, point in time), then what a restore does, in one line,
 * and one list of restore points, newest first, with a kind column. One
 * point is marked latest: the newest usable dump, else snapshot (never a
 * failed or empty one; two "latest" read as a choice). Restore shows on
 * every point that restores, Retry on the newest failed one; View log and
 * Delete are in each row's menu. A template database's Restore opens one
 * dialog (into a new service); any other service's asks under the
 * point's row and restores in place (ADR 0095). A Postgres database with
 * point in time on (ADR 0093) has its window over the list.
 */
export function BackupsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const backups = useBackups(ref);
  const snaps = useSnapshots(ref);
  const canDeploy = useCanDeploy(ref);
  const run = useRunBackup();
  const [open, setOpen] = useState<Open | null>(null);
  const [askSnapshot, setAskSnapshot] = useState(false);
  const { environment } = useEnvironment(ref);
  const close = () => setOpen(null);
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  // A template database restores into a new service, from either kind.
  const intoNew = snaps.into_new;
  const points = restorePoints(backups, snaps.snapshots, environment.server);
  const records = restoreRecords(backups, snaps.restores, snaps.snapshots);
  const busy =
    points.some((p) => p.state === "running") ||
    records.some((r) => r.status === "running");
  const why = !canDeploy
    ? "needs deploy access"
    : busy
      ? "a backup, snapshot or restore is running"
      : undefined;
  const restorable = (p: RestorePoint) =>
    p.state === "ready" &&
    !isRestoreRun(p) &&
    (p.kind === "snapshot" ||
      ((backups.restorable || backups.restores_in_place) &&
        !p.run.not_restorable &&
        !isBaseRun(p.run)));
  // In place is asked under the point's row; a new service has its dialog.
  const inPlace = (p: RestorePoint) =>
    p.kind === "backup" ? !backups.restorable : !intoNew;
  // Point in time, for Postgres: drawn from the data at this render.
  const pitr = backups.pitr;
  const now = Date.now();
  const window = pitr && windowOf(pitr, now);
  const marks = pitr && stripMarks(pitr, backups.runs, snaps.snapshots, now);
  // The newest usable point of each kind, and the newest of each kind
  // whatever its state (Retry is its). The one latest is the dump's, else
  // the snapshot's.
  const newestOf = (kind: RestorePoint["kind"], usable: boolean) =>
    points.find(
      (p) =>
        p.kind === kind &&
        (usable ? restorable(p) && !isEmpty(p) : !isRestoreRun(p)),
    )?.key;
  const latest = newestOf("backup", true) ?? newestOf("snapshot", true);
  const newest = new Set([
    newestOf("backup", false),
    newestOf("snapshot", false),
  ]);
  const lastGood = points.find(
    (p) => p.kind === "backup" && restorable(p) && !isEmpty(p),
  );
  // The Restore to lead with: the latest point. It leads only into a new
  // service, which changes nothing live, and gives way to a restore to a
  // time.
  const lead = intoNew && !window ? latest : undefined;
  // The place column only when points are kept in more than one place.
  const places = new Set(points.map((p) => p.where).filter(Boolean));
  const showWhere = places.size > 1;
  // #backup-<id> (a failed backup's alert links there): scroll to its row
  // and open its log, once per hash.
  const hash = useLocation({ select: (l) => l.hash });
  // Points change with live events; only the hash decides when this runs.
  useEffect(() => {
    const id = /^backup-(\d+)$/.exec(hash)?.[1];
    const point = id
      ? points.find((p) => p.kind === "backup" && p.run.id === Number(id))
      : undefined;
    if (!point) return;
    document.getElementById(hash)?.scrollIntoView({ block: "center" });
    setOpen({ what: "log", point });
  }, [hash]);

  return (
    <SettingsSection
      id="backups"
      title="Backups"
      actions={
        <>
          <Act
            cli={`dock volume snapshot ${flags}`}
            disabled={!!why}
            title={`${ref.service} pauses while its volume is copied`}
            onClick={() => setAskSnapshot(true)}
          >
            Snapshot the volume
          </Act>
          <Act
            primary
            cli={`dock backup run ${flags}`}
            disabled={!!why || run.isPending}
            reason={why}
            onClick={() => run.mutate(ref)}
          >
            Back up now
          </Act>
          <ErrorText error={run.error} />
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <dl className="props">
          {/* On the middle line of the row's controls (the 28px hour
              select, or the 24px checkbox alone), not a 34px field's (the
              .props rule matches the select). */}
          <dt className={backups.schedule.enabled ? "pt-[13px]" : "pt-[11px]"}>
            Schedule
          </dt>
          <dd className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Schedule serviceRef={ref} backups={backups} />
            {lastGood ? (
              <span className="text-ok">last good {ago(lastGood.at)}</span>
            ) : (
              <span className="text-warn">no good backup yet</span>
            )}
          </dd>
          <dt>Kept</dt>
          <dd>
            {backupWhat[backups.kind]}, <Where backups={backups} />
            {backups.warning && (
              <span className="block text-warn">{backups.warning}</span>
            )}
          </dd>
          <dt>Snapshots</dt>
          <dd>
            Copies of its volumes on this server; {ref.service} pauses while one
            is taken. Keeps {plural(snaps.keep, "snapshot")} for{" "}
            {plural(snaps.keep_days, "day")}.
          </dd>
          <CheckedLine serviceRef={ref} backups={backups} />
          {pitr && (
            <>
              <dt>Point in time</dt>
              <dd>
                <PitrRow
                  serviceRef={ref}
                  pitr={pitr}
                  onSettings={(mode) => setOpen({ what: "pitr", mode })}
                />
              </dd>
            </>
          )}
        </dl>
        {askSnapshot && (
          <TakeSnapshot
            serviceRef={ref}
            production={environment.kind === "production"}
            onDone={() => setAskSnapshot(false)}
          />
        )}
        {records[0] && (
          <RestoreStatus
            key={records[0].key}
            serviceRef={ref}
            record={records[0]}
            canSwitch={canDeploy}
            onUse={(to) =>
              setOpen({ what: "switch", to, since: records[0]?.pointAt })
            }
          />
        )}
        {pitr?.enabled && marks && (
          <PitrSection
            serviceRef={ref}
            pitr={pitr}
            runs={backups.runs}
            marks={marks}
            window={window}
            why={why}
            onRestore={(at) => setOpen({ what: "time", at })}
          />
        )}
        <h3 className="mt-3 flex flex-wrap items-baseline gap-x-3 font-semibold">
          Restore points
          <span className="font-normal text-mut">
            {intoNew
              ? `A restore goes into a new service beside ${ref.service}; ${ref.service} keeps running.`
              : `A restore goes in place: ${ref.service} stops, a safety snapshot keeps what it holds now, then the copy goes in.`}
          </span>
        </h3>
        <Scroll>
          <table>
            <thead>
              <tr>
                <th>Taken</th>
                <th>Kind</th>
                <th>Restore point</th>
                <th>Trigger</th>
                <th className="text-right">Size</th>
                {showWhere && <th>Where</th>}
                <th className="w-full">Detail</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {points.length === 0 && (
                <tr>
                  <td colSpan={columns} className="text-mut">
                    None yet. The first daily backup runs at{" "}
                    {hourLabel(backups.schedule.hour)}, or Back up now.
                  </td>
                </tr>
              )}
              {points.map((p) => (
                <PointRow
                  key={p.key}
                  serviceRef={ref}
                  point={p}
                  kind={
                    isRestoreRun(p)
                      ? "restore"
                      : p.kind === "snapshot"
                        ? "snapshot"
                        : backupKind[backups.kind]
                  }
                  showWhere={showWhere}
                  latest={p.key === latest}
                  lead={p.key === lead}
                  restore={
                    restorable(p)
                      ? () => setOpen({ what: "restore", point: p })
                      : undefined
                  }
                  asking={
                    open?.what === "restore" &&
                    open.point.key === p.key &&
                    inPlace(p)
                  }
                  onClose={close}
                  why={why}
                  volumes={
                    p.kind === "snapshot" ? p.snapshot.volumes : backups.volumes
                  }
                  undo={
                    // Linked while the snapshot is still in the list.
                    p.kind === "backup" &&
                    snaps.snapshots.some((s) => s.id === p.run.safety_snapshot)
                      ? p.run.safety_snapshot
                      : undefined
                  }
                  onLog={
                    canDeploy &&
                    (p.kind === "backup"
                      ? p.state !== "running"
                      : p.state === "failed")
                      ? () => setOpen({ what: "log", point: p })
                      : undefined
                  }
                  onRetry={
                    // On the newest point of a kind only, when it failed.
                    p.state === "failed" && newest.has(p.key) && !why
                      ? () =>
                          p.kind === "backup"
                            ? run.mutate(ref)
                            : setAskSnapshot(true)
                      : undefined
                  }
                  onDelete={
                    p.kind === "snapshot" && p.state !== "running" && canDeploy
                      ? () => setOpen({ what: "delete", point: p })
                      : undefined
                  }
                />
              ))}
            </tbody>
          </table>
        </Scroll>
      </div>
      <Suspense fallback={null}>
        {open?.what === "restore" && !inPlace(open.point) && (
          <RestoreDialog
            serviceRef={ref}
            point={open.point}
            onClose={close}
            onStarted={close}
          />
        )}
        {open?.what === "log" && (
          <LogDialog serviceRef={ref} point={open.point} onClose={close} />
        )}
        {open?.what === "time" && pitr && marks && window && (
          <TimeRestoreDialog
            serviceRef={ref}
            pitr={pitr}
            runs={backups.runs}
            marks={marks}
            window={window}
            initial={open.at}
            onClose={close}
            onStarted={close}
          />
        )}
        {open?.what === "pitr" && pitr && (
          <PitrDialog
            serviceRef={ref}
            pitr={pitr}
            hour={backups.schedule.enabled ? backups.schedule.hour : undefined}
            mode={open.mode}
            onClose={close}
          />
        )}
        {open?.what === "switch" && (
          <SwitchDialog
            serviceRef={ref}
            to={open.to}
            since={open.since}
            onClose={close}
          />
        )}
      </Suspense>
      {open?.what === "delete" && open.point.kind === "snapshot" && (
        <DeleteSnapshot
          serviceRef={ref}
          id={open.point.snapshot.id}
          onDone={close}
        />
      )}
    </SettingsSection>
  );
}

/**
 * Asks before taking a snapshot, with its note: the service pauses while
 * its volume is copied, which production says first and loudest.
 */
function TakeSnapshot({
  serviceRef: ref,
  production,
  onDone,
}: {
  serviceRef: ServiceRef;
  production: boolean;
  onDone: () => void;
}) {
  const take = useTakeSnapshot();
  const [note, setNote] = useState("");
  const cli = `dock volume snapshot -s ${ref.service} -e ${ref.environment}${note.trim() ? ` --note ${JSON.stringify(note.trim())}` : ""}`;
  return (
    <Confirm
      title={`Take a snapshot of ${ref.service} in ${ref.environment}?`}
      action="Take snapshot"
      cli={cli}
      pending={take.isPending}
      error={take.error}
      onConfirm={() =>
        take.mutate({ ref, note: note.trim() }, { onSuccess: onDone })
      }
      onCancel={() => {
        take.reset();
        onDone();
      }}
    >
      <span className="flex flex-col gap-3">
        <span>
          {production ? (
            <b>
              {ref.service} pauses while its volume is copied, usually a few
              seconds.
            </b>
          ) : (
            `${ref.service} pauses while its volume is copied, usually a few seconds.`
          )}{" "}
          Requests to it wait meanwhile.
        </span>
        <input
          aria-label="Snapshot note"
          placeholder="Note (optional), like: before the migration"
          maxLength={200}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          className="field w-full text-fg"
        />
      </span>
    </Confirm>
  );
}

/** Kept off-site, or on this server only with the way to change that. */
function Where({ backups }: { backups: ServiceBackups }) {
  const operator = useIsOperator();
  if (backups.off_box) return <>kept off-site</>;
  return (
    <span title="dock backup configure">
      <span className="text-warn">kept on this server only</span>
      {operator && (
        <>
          {" · "}
          <Link
            to="/console/backups"
            className="text-info underline underline-offset-2"
          >
            Add off-site storage
          </Link>
        </>
      )}
      {!operator && (
        <span className="text-mut">
          {" "}
          · an operator can add off-site storage
        </span>
      )}
    </span>
  );
}

/**
 * Daily on or off, the hour, and when the next one runs. Both save on
 * change and say Saved. Backups are the platform's: operators set
 * schedules (ADR 0027).
 */
function Schedule({
  serviceRef: ref,
  backups: { schedule },
}: {
  serviceRef: ServiceRef;
  backups: ServiceBackups;
}) {
  const operator = useIsOperator();
  const update = useBackupSchedule();
  const busy = !operator || update.isPending;
  const cli = `dock backup schedule ${ref.service} -e ${ref.environment}`;
  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span title={`${cli} --${schedule.enabled ? "off" : "on"}`}>
        <Check
          label="daily"
          checked={schedule.enabled}
          disabled={busy}
          onChange={(enabled) => update.mutate({ ref, enabled })}
        />
      </span>
      {schedule.enabled && (
        <>
          <label className="flex items-center gap-1.5">
            <span className="text-mut">at</span>
            <select
              aria-label="Hour"
              title={`${cli} --hour N, or --default-hour`}
              value={schedule.default_hour ? "default" : schedule.hour}
              disabled={busy}
              onChange={(e) =>
                update.mutate(
                  e.target.value === "default"
                    ? { ref, default_hour: true }
                    : { ref, hour: Number(e.target.value) },
                )
              }
            >
              <option value="default">
                {schedule.default_hour
                  ? `${hourLabel(schedule.hour)} (default)`
                  : "Default hour"}
              </option>
              {Array.from({ length: 24 }, (_, h) => (
                <option key={h} value={h}>
                  {hourLabel(h)}
                </option>
              ))}
            </select>
          </label>
          {schedule.next_at && (
            <span className="text-mut">next in {until(schedule.next_at)}</span>
          )}
        </>
      )}
      <Saved ok={update.isSuccess} />
      <ErrorText error={update.error} />
    </span>
  );
}

const pointStates: Record<PointState, State> = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  ready: { tone: "ok", glyph: "●", label: "" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
  removed: { tone: "mut", label: "removed" },
};

/** A restore that went well: a point that is ready has no state to say. */
const restoreDone: State = { tone: "ok", glyph: "●", label: "done" };

/** An empty cell's hyphen, so a missing value reads as missing. */
const none = <span className="text-faint">-</span>;

/** The table's most columns (with Where), for rows that span them. */
const columns = 8;

/**
 * One row of the list: when, what (a backup or snapshot by id, or a
 * restore of one), its trigger, size, where and its note or error, with
 * its actions in fixed slots at the right edge. Failed and removed points
 * are muted: they can't be restored. An empty one says so in its size.
 * While `asking`, the in-place restore's question sits under the row. An
 * in-place restore that went well links the snapshot that undoes it.
 */
function PointRow({
  serviceRef: ref,
  point: p,
  kind,
  showWhere,
  latest,
  lead,
  restore,
  asking,
  onClose,
  why,
  volumes,
  undo,
  onLog,
  onRetry,
  onDelete,
}: {
  serviceRef: ServiceRef;
  point: RestorePoint;
  /** What it is, in the Kind column: dump, file backup, snapshot, restore. */
  kind: string;
  showWhere: boolean;
  latest: boolean;
  /** The Restore to lead with: its button is the primary one. */
  lead: boolean;
  /** Asks about the restore; unset where the point can't be restored. */
  restore?: () => void;
  /** The in-place question is open under this row. */
  asking: boolean;
  /** Closes that question. */
  onClose: () => void;
  /** Why Restore waits, when it does. */
  why?: string;
  /** The volumes an in-place restore of the point replaces. */
  volumes?: string[] | null;
  /** The safety snapshot of an in-place restore, in the list below. */
  undo?: number;
  onLog?: () => void;
  onRetry?: () => void;
  onDelete?: () => void;
}) {
  const good = p.state === "ready";
  const restored = isRestoreRun(p);
  return (
    <>
      <PointCells
        point={p}
        kind={kind}
        showWhere={showWhere}
        good={good}
        restored={restored}
        latest={latest}
        asking={asking}
        undo={undo}
      >
        {/* Its one visible action (Restore, else Retry), then its menu. */}
        <td className="py-1 text-right whitespace-nowrap">
          <span className="inline-flex items-center gap-2">
            {restore ? (
              asking ? (
                <Button small onClick={onClose}>
                  Close
                </Button>
              ) : (
                <Button
                  small
                  primary={lead && !why}
                  disabled={!!why}
                  title={why}
                  onClick={restore}
                >
                  Restore
                </Button>
              )
            ) : (
              onRetry && (
                <Button small onClick={onRetry}>
                  Retry
                </Button>
              )
            )}
            {(onLog || onDelete || (restore && onRetry)) && (
              <MoreMenu label={`${pointName(p)} actions`}>
                {(close) => (
                  <>
                    {onLog && (
                      <MoreItem close={close} onClick={onLog}>
                        View log
                      </MoreItem>
                    )}
                    {restore && onRetry && (
                      <MoreItem close={close} onClick={onRetry}>
                        Retry
                      </MoreItem>
                    )}
                    {onDelete && (
                      <>
                        {onLog && <MoreSeparator />}
                        <MoreItem close={close} danger onClick={onDelete}>
                          Delete {pointName(p)}…
                        </MoreItem>
                      </>
                    )}
                  </>
                )}
              </MoreMenu>
            )}
          </span>
        </td>
      </PointCells>
      {asking && (
        <tr>
          <td colSpan={columns} className="bg-panel whitespace-normal">
            <RestoreInPlace
              serviceRef={ref}
              point={p}
              volumes={volumes ?? []}
              why={why}
              onClose={onClose}
            />
          </td>
        </tr>
      )}
    </>
  );
}

/** A row's text cells, then its action cells (`children`). */
function PointCells({
  point: p,
  kind,
  showWhere,
  good,
  restored,
  latest,
  asking,
  undo,
  children,
}: {
  point: RestorePoint;
  kind: string;
  showWhere: boolean;
  good: boolean;
  /** A restore of a backup, not a point. */
  restored: boolean;
  latest: boolean;
  asking: boolean;
  /** The safety snapshot that undoes this restore: its row is linked. */
  undo?: number;
  children: ReactNode;
}) {
  return (
    <tr
      id={
        p.kind === "backup" ? `backup-${p.run.id}` : `snapshot-${p.snapshot.id}`
      }
      className={`scroll-mt-24 ${good ? "" : "text-mut"} ${asking ? "bg-panel [&>td]:border-b-transparent" : ""}`}
    >
      <td className="whitespace-nowrap" title={`${day(p.at)} ${clock(p.at)}`}>
        {ago(p.at)}
        {latest && <span className="text-ok"> latest</span>}
      </td>
      <td className="whitespace-nowrap">{kind}</td>
      {/* A restore's name is long and it has no size or place: it wraps. */}
      <td
        className={
          restored ? "min-w-[200px] whitespace-normal" : "whitespace-nowrap"
        }
      >
        {(restored || !good) && (
          <>
            <StateText
              state={restored && good ? restoreDone : pointStates[p.state]}
            />{" "}
          </>
        )}
        <span className={good ? "text-fg" : ""}>{pointName(p)}</span>
      </td>
      <td className="whitespace-nowrap text-mut">
        {/* Manual backups stand out: they are kept apart from the daily ones. */}
        <span
          className={
            good && p.kind === "backup" && p.trigger === "manual"
              ? "text-info"
              : ""
          }
        >
          {[p.trigger, p.by !== "system" && `by ${actorLabel(p.by)}`]
            .filter(Boolean)
            .join(" ") || none}
        </span>
      </td>
      <td className="text-right font-mono">
        {isEmpty(p) ? (
          <span className="font-sans text-warn" title="It holds no data">
            empty
          </span>
        ) : p.size > 0 ? (
          size(p.size)
        ) : (
          none
        )}
      </td>
      {/* Only when points are kept in more than one place: "kept on this
          server only" above says the risk once. */}
      {showWhere && (
        <td
          className="whitespace-nowrap"
          title={p.server ? `Kept on server ${p.server}` : undefined}
        >
          {p.where || none}
        </td>
      )}
      {/* A floor, so long names beside it never squeeze an error to a word a line. */}
      <td className="max-w-[420px] min-w-[180px] whitespace-normal">
        {p.detail ? (
          <span className={p.state === "failed" ? "text-fail" : ""}>
            {p.detail}
          </span>
        ) : undo ? (
          <span className="text-mut">
            replaced data kept in{" "}
            <a
              href={`#snapshot-${undo}`}
              className="text-info underline underline-offset-2"
              title="Restoring it undoes this restore"
              onClick={(e) => {
                e.preventDefault();
                document
                  .getElementById(`snapshot-${undo}`)
                  ?.scrollIntoView({ block: "center" });
              }}
            >
              snapshot {undo}
            </a>
          </span>
        ) : (
          none
        )}
      </td>
      {children}
    </tr>
  );
}

/**
 * The question before an in-place restore, under the point's row: which
 * point, what happens to the service and its data (the volumes it
 * replaces, by name), and what undoes it.
 * Nothing changes until Restore in place; what the API refuses (a deploy
 * or another restore is running) shows here. The restore then runs as a
 * row of the list. A backup also restores into a copy of the service
 * under another name, which touches nothing (ADR 0095).
 */
function RestoreInPlace({
  serviceRef: ref,
  point: p,
  volumes,
  why,
  onClose,
}: {
  serviceRef: ServiceRef;
  point: RestorePoint;
  /**
   * The volumes replaced: the ones a snapshot holds, or for a backup the
   * service's volumes now (a backup doesn't list its own).
   */
  volumes: string[];
  /** Why it can't start now, when it can't. */
  why?: string;
  onClose: () => void;
}) {
  const backup = useRestoreBackup();
  const snapshot = useRestoreSnapshot();
  const pending = backup.isPending || snapshot.isPending;
  const [copy, setCopy] = useState(`${ref.service}-restored`);
  const copyOf = p.kind === "backup" ? p.run.snapshot : undefined;
  const taken = `${day(p.at)} ${clock(p.at)}`;
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const cli =
    p.kind === "backup"
      ? `dock backup restore ${ref.service} --snapshot ${p.run.snapshot?.slice(0, 8)} --in-place -e ${ref.environment}`
      : `dock volume snapshot restore ${p.snapshot.id} ${flags} --in-place`;
  const start = () => {
    const done = { onSuccess: onClose };
    if (p.kind === "snapshot") {
      snapshot.mutate({ ref, id: p.snapshot.id, in_place: true }, done);
    } else if (p.run.snapshot) {
      backup.mutate({ ref, snapshot: p.run.snapshot, in_place: true }, done);
    }
  };
  return (
    <form
      aria-label={`Restore ${ref.service} from ${pointName(p)}`}
      className="flex flex-col gap-2 py-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (!pending && !why) start();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") onClose();
      }}
    >
      <p className="text-sec">
        <span className="text-fg">{pointName(p)}</span> · {taken} · {ago(p.at)}{" "}
        · {isEmpty(p) ? "empty" : size(p.size)}
        {p.trigger && ` · ${p.trigger}`}
      </p>
      {isEmpty(p) && (
        <p className="text-warn">
          This {p.kind} holds no data (0 B): {ref.service} would be left empty.
          Pick a point with a size instead.
        </p>
      )}
      <dl className="grid grid-cols-[76px_minmax(0,1fr)] gap-y-1 text-sec">
        <dt className="text-warn">Stops</dt>
        <dd>{ref.service} is down until the restore ends.</dd>
        <dt className="text-warn">Replaced</dt>
        <dd>
          {volumes.length === 0 ? (
            `The volumes in the ${p.kind} are emptied and filled from it.`
          ) : (
            <>
              {volumes.length === 1 ? "Volume " : "Volumes "}
              <span className="font-mono text-fg">{volumes.join(", ")}</span>
              {volumes.length === 1 ? " is" : " are"} emptied and filled from
              the {p.kind}.
            </>
          )}{" "}
          Changes since {taken} are lost.
          {p.kind === "backup" &&
            volumes.length > 1 &&
            " A volume added after the backup was taken is left as it is."}
        </dd>
        <dt className="text-ok">Kept</dt>
        <dd>
          A safety snapshot of what they hold now. Restoring it undoes this.
        </dd>
        {copyOf && (
          <>
            <dt>Or a copy</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <span>
                A new service with the backup's data. {ref.service} keeps
                running.
              </span>
              <input
                aria-label="Name of the copy"
                maxLength={32}
                value={copy}
                onChange={(e) => setCopy(e.target.value)}
                className="field w-48 py-[3px] font-mono text-fg"
              />
              <Button
                small
                disabled={pending || !copy.trim()}
                onClick={() =>
                  backup.mutate(
                    { ref, snapshot: copyOf, as: copy.trim() },
                    { onSuccess: onClose },
                  )
                }
              >
                Restore into a copy
              </Button>
            </dd>
          </>
        )}
      </dl>
      <div className="flex flex-wrap items-center justify-end gap-2">
        <code className="mr-auto font-mono text-2xs text-faint">{cli}</code>
        <ErrorText error={backup.error ?? snapshot.error} />
        <Button small onClick={onClose}>
          Cancel
        </Button>
        <Button
          small
          primary
          type="submit"
          disabled={pending || !!why}
          reason={why}
        >
          Restore in place
        </Button>
      </div>
    </form>
  );
}

/** Restores dismissed here, so a status row stays gone after a reload. */
const dismissedKey = "dock.restores.dismissed";
const dismissed = () => {
  try {
    return new Set<string>(
      JSON.parse(localStorage.getItem(dismissedKey) ?? "[]"),
    );
  } catch {
    return new Set<string>();
  }
};

/**
 * The latest restore as a status row until it is dismissed: running,
 * failed, or done ("✓ Restored backup 9d03b7a1 into db-restored") with
 * Open and Use for a new service that still exists, or the undo for an
 * in-place one. Announced politely as it changes.
 */
function RestoreStatus({
  serviceRef: ref,
  record: r,
  canSwitch,
  onUse,
}: {
  serviceRef: ServiceRef;
  record: RestoreRecord;
  canSwitch: boolean;
  onUse: (to: string) => void;
}) {
  const org = useOrg();
  const { services } = useEnvironment(ref);
  const id = `${ref.project}/${ref.environment}/${ref.service}/${r.key}`;
  const [gone, setGone] = useState(() => dismissed().has(id));
  const target = r.to
    ? services.find((s) => s.service.name === r.to)?.service.name
    : undefined;
  const into = r.to ? `into ${r.to}` : "in place";
  if (gone) return null;
  const dismiss = () => {
    const all = dismissed();
    all.add(id);
    // The newest few are enough to remember.
    localStorage.setItem(dismissedKey, JSON.stringify([...all].slice(-20)));
    setGone(true);
  };
  return (
    <div
      aria-live="polite"
      className={`flex flex-wrap items-center gap-x-3 gap-y-2 rounded-panel border px-3.5 py-2.5 ${r.status === "failed" ? "border-fail-edge" : "border-line"}`}
    >
      <span className="min-w-0 flex-1 text-fg">
        {r.status === "running" ? (
          <>
            <span className="text-prog">◐</span> Restoring {r.from} {into}
          </>
        ) : r.status === "failed" ? (
          <>
            <span className="text-fail">✕</span> Restoring {r.from} {into}{" "}
            failed: <span className="text-fail">{r.error}</span>
          </>
        ) : (
          <>
            <span className="text-ok">✓</span>{" "}
            {target && canSwitch && (
              <b className="font-semibold">Step 1 of 2 done: </b>
            )}
            Restored {r.from} {into}{" "}
            <span className="text-mut">{ago(r.at)}</span>
            {r.safety ? (
              <span className="text-mut">
                . Undo: restore snapshot {r.safety}.
              </span>
            ) : null}
            {/* A copy changes nothing yet: say what still runs, and the step that switches. */}
            {target && (
              <span className="mt-1 block text-warn-text">
                {canSwitch && <b className="font-semibold">Step 2: </b>}
                {ref.service} still holds the old data, and the services that
                use it still read {ref.service}.
                {canSwitch &&
                  ` Use ${target} points them at the restored copy.`}
              </span>
            )}
          </>
        )}
      </span>
      {r.status === "succeeded" && target && canSwitch && (
        <Button primary small onClick={() => onUse(target)}>
          Use {target}
        </Button>
      )}
      {target && (
        <Link
          to="/$org/$project/$environment/$service"
          params={{ org, ...ref, service: target }}
          className={`btn sm ${r.status === "succeeded" && canSwitch ? "" : "pri"}`}
        >
          Open {target}
        </Link>
      )}
      {r.status !== "running" && (
        <Button small onClick={dismiss}>
          {/* Before step 2, closing it means keeping the old data. */}
          {r.status === "succeeded" && target && canSwitch
            ? `Keep using ${ref.service}`
            : "Dismiss"}
        </Button>
      )}
    </div>
  );
}

/** Asks before deleting a snapshot's files. */
function DeleteSnapshot({
  serviceRef: ref,
  id,
  onDone,
}: {
  serviceRef: ServiceRef;
  id: number;
  onDone: () => void;
}) {
  const remove = useDeleteSnapshot();
  return (
    <Confirm
      danger
      action={`Delete snapshot ${id}`}
      cli={`dock volume snapshot delete ${id} -s ${ref.service} -e ${ref.environment}`}
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate({ ref, id }, { onSuccess: onDone })}
      onCancel={() => {
        remove.reset();
        onDone();
      }}
    >
      Its files on the server are removed. This can't be undone.
    </Confirm>
  );
}
