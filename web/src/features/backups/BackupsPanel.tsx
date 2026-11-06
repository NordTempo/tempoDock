import { Link, useLocation } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { useBackupSchedule, useRunBackup } from "../../data/mutations";
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
  Button,
  Check,
  Confirm,
  ErrorText,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import type { State } from "../../ui/status";
import { Saved } from "../create/form";
import { backupWhat, hourLabel } from "./backups";
import { useDeleteSnapshot, useSnapshots, useTakeSnapshot } from "./data";
import {
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

/** What the tab has open: one dialog at a time. */
type Open =
  | { what: "restore" | "log" | "delete"; point: RestorePoint }
  | { what: "switch"; to: string };

/**
 * A stateful service's restore points (ADR 0061): how backups and
 * snapshots are made, then both in one list, newest first, the newest
 * good one marked latest and failed runs secondary. One Restore per point
 * opens one dialog; the latest restores into new services offer Use.
 */
export function BackupsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const backups = useBackups(ref);
  const snaps = useSnapshots(ref);
  const canDeploy = useCanDeploy(ref);
  const run = useRunBackup();
  const take = useTakeSnapshot();
  const [note, setNote] = useState("");
  const [open, setOpen] = useState<Open | null>(null);
  // Taking a snapshot pauses the service: production asks first.
  const production = useEnvironment(ref).environment.kind === "production";
  const [askSnapshot, setAskSnapshot] = useState(false);
  const takeNow = () =>
    take.mutate(
      { ref, note: note.trim() },
      {
        onSuccess: () => {
          setNote("");
          setAskSnapshot(false);
        },
      },
    );
  const close = () => setOpen(null);
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  // A template database restores into a new service, from either kind.
  const intoNew = snaps.into_new;
  const points = restorePoints(backups, snaps.snapshots);
  const records = restoreRecords(backups, snaps.restores);
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
    (p.kind === "snapshot" || (backups.restorable && !p.run.not_restorable));
  const latest = points.find(restorable)?.key;
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
  // Retry sits on the newest run of a kind only, when that one failed.
  const newest = (kind: RestorePoint["kind"]) =>
    points.find((p) => p.kind === kind)?.key;

  return (
    <section aria-labelledby="restore-points" className="flex flex-col gap-3">
      <SectionHead
        id="restore-points"
        right={
          <form
            aria-label="Take snapshot"
            className="flex flex-wrap items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (production) setAskSnapshot(true);
              else takeNow();
            }}
          >
            <input
              aria-label="Snapshot note"
              placeholder="note, optional"
              maxLength={200}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              className="field w-48 py-[3px] text-fg"
            />
            <Act
              small
              type="submit"
              cli={`dock volume snapshot ${flags}${note.trim() ? ` --note ${JSON.stringify(note.trim())}` : ""}`}
              disabled={!!why || take.isPending}
              reason={why}
              title={`${ref.service} pauses while its volume is copied`}
            >
              Take snapshot
            </Act>
            <Act
              small
              cli={`dock backup run ${flags}`}
              disabled={!!why || run.isPending}
              onClick={() => run.mutate(ref)}
            >
              Back up now
            </Act>
            <ErrorText error={take.error ?? run.error} />
          </form>
        }
      >
        Restore points
      </SectionHead>
      <dl className="props grid-cols-[120px_minmax(0,1fr)]">
        <dt>Backups</dt>
        <dd>
          {backupWhat[backups.kind]}, <Where backups={backups} />
          {backups.warning && (
            <span className="block text-warn">{backups.warning}</span>
          )}
        </dd>
        <dt>Schedule</dt>
        <dd>
          <Schedule serviceRef={ref} backups={backups} />
        </dd>
        <dt>Snapshots</dt>
        <dd>
          volume copies on {snaps.snapshots[0]?.server ?? "its server"}, the
          service paused while copied; keeps {plural(snaps.keep, "snapshot")}{" "}
          for {plural(snaps.keep_days, "day")}
        </dd>
      </dl>
      {askSnapshot && (
        <Confirm
          title={`Take a snapshot of ${ref.service} in ${ref.environment}?`}
          action="Take snapshot"
          cli={`dock volume snapshot ${flags}${note.trim() ? ` --note ${JSON.stringify(note.trim())}` : ""}`}
          pending={take.isPending}
          error={take.error}
          onConfirm={takeNow}
          onCancel={() => {
            take.reset();
            setAskSnapshot(false);
          }}
        >
          <b>
            {ref.service} pauses while its volume is copied, usually a few
            seconds.
          </b>{" "}
          Requests to it wait meanwhile.
        </Confirm>
      )}
      {records[0] && (
        <RestoreStatus
          key={records[0].key}
          serviceRef={ref}
          record={records[0]}
          canSwitch={canDeploy}
          onUse={(to) => setOpen({ what: "switch", to })}
        />
      )}
      {points.length === 0 ? (
        <p className="text-mut">No backups or snapshots yet.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Taken</th>
                <th>Restore point</th>
                <th>Size</th>
                <th>Where</th>
                <th className="w-full">Detail</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {points.map((p) => (
                <PointRow
                  key={p.key}
                  point={p}
                  latest={p.key === latest}
                  restore={
                    restorable(p)
                      ? () => setOpen({ what: "restore", point: p })
                      : undefined
                  }
                  why={why}
                  onLog={
                    canDeploy &&
                    (p.kind === "backup"
                      ? p.state !== "running"
                      : p.state === "failed")
                      ? () => setOpen({ what: "log", point: p })
                      : undefined
                  }
                  onRetry={
                    p.state === "failed" && p.key === newest(p.kind) && !why
                      ? () =>
                          p.kind === "backup"
                            ? run.mutate(ref)
                            : take.mutate({ ref, note: p.snapshot.note ?? "" })
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
        </Panel>
      )}
      <Suspense fallback={null}>
        {open?.what === "restore" && (
          <RestoreDialog
            serviceRef={ref}
            point={open.point}
            intoNew={intoNew}
            onClose={close}
            onStarted={close}
          />
        )}
        {open?.what === "log" && (
          <LogDialog serviceRef={ref} point={open.point} onClose={close} />
        )}
        {open?.what === "switch" && (
          <SwitchDialog serviceRef={ref} to={open.to} onClose={close} />
        )}
      </Suspense>
      {open?.what === "delete" && open.point.kind === "snapshot" && (
        <DeleteSnapshot
          serviceRef={ref}
          id={open.point.snapshot.id}
          onDone={close}
        />
      )}
    </section>
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
          <Link to="/console/backups" className="text-info hover:underline">
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

/** An empty cell's hyphen, so a missing value reads as missing. */
const none = <span className="text-faint">-</span>;

/**
 * One restore point: when, what (kind, id, trigger), size, where and its
 * note or error, with its actions at the row's right edge. Failed and
 * removed points are muted: they can't be restored.
 */
function PointRow({
  point: p,
  latest,
  restore,
  why,
  onLog,
  onRetry,
  onDelete,
}: {
  point: RestorePoint;
  latest: boolean;
  /** Opens the restore dialog; unset where the point can't be restored. */
  restore?: () => void;
  /** Why Restore waits, when it does. */
  why?: string;
  onLog?: () => void;
  onRetry?: () => void;
  onDelete?: () => void;
}) {
  const good = p.state === "ready";
  return (
    <tr
      id={
        p.kind === "backup" ? `backup-${p.run.id}` : `snapshot-${p.snapshot.id}`
      }
      className={`scroll-mt-24 ${good ? "" : "text-mut"}`}
    >
      <td className="whitespace-nowrap" title={`${day(p.at)} ${clock(p.at)}`}>
        {ago(p.at)}
        {latest && <span className="text-ok"> latest</span>}
      </td>
      <td className="whitespace-nowrap">
        {p.state !== "ready" && (
          <>
            <StateText state={pointStates[p.state]} />{" "}
          </>
        )}
        <span className={good ? "text-fg" : ""}>{pointName(p)}</span>
        <span className="text-mut">
          {" "}
          · {p.trigger}
          {p.by !== "system" && ` by ${p.by}`}
        </span>
      </td>
      <td className="font-mono">{p.size > 0 ? size(p.size) : none}</td>
      <td
        className={
          p.kind === "backup" && p.where !== "off-site"
            ? "whitespace-nowrap text-warn"
            : "whitespace-nowrap"
        }
      >
        {p.where}
      </td>
      <td className="max-w-[420px] whitespace-normal">
        {p.detail ? (
          <span className={p.state === "failed" ? "text-fail" : ""}>
            {p.detail}
          </span>
        ) : (
          none
        )}
      </td>
      <td className="whitespace-nowrap">
        <span className="flex items-center justify-end gap-1.5">
          {onLog && (
            <Button small onClick={onLog}>
              View log
            </Button>
          )}
          {onRetry && (
            <Button small onClick={onRetry}>
              Retry
            </Button>
          )}
          {onDelete && (
            <Button small onClick={onDelete}>
              Delete
            </Button>
          )}
          {restore && (
            <Button
              small
              primary={latest}
              disabled={!!why}
              reason={why}
              onClick={restore}
            >
              Restore
            </Button>
          )}
        </span>
      </td>
    </tr>
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
            <span className="text-ok">✓</span> Restored {r.from} {into}{" "}
            <span className="text-mut">{ago(r.at)}</span>
            {r.safety ? (
              <span className="text-mut">
                . Undo: restore snapshot {r.safety}.
              </span>
            ) : null}
          </>
        )}
      </span>
      {target && (
        <Link
          to="/$org/$project/$environment/$service"
          params={{ org, ...ref, service: target }}
          className="btn sm"
        >
          Open {target}
        </Link>
      )}
      {r.status === "succeeded" && target && canSwitch && (
        <Button small primary onClick={() => onUse(target)}>
          Use {target}
        </Button>
      )}
      {r.status !== "running" && (
        <Button small onClick={dismiss} aria-label="Dismiss">
          Dismiss
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
