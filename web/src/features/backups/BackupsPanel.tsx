import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  useBackupSchedule,
  useRestoreBackup,
  useRunBackup,
} from "../../data/mutations";
import { useBackups, useCanDeploy, useIsOperator } from "../../data/queries";
import type { BackupRun, ServiceBackups, ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { ago, clock, day, size, until } from "../../ui/format";
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
import { backupHow, exportOf, hourLabel } from "./backups";
import { backupLogQuery } from "./data";

/**
 * A stateful service's backups (ADR 0019): how and where they go, the
 * daily schedule, Run now, and the latest runs, newest first, with View
 * log and, for template databases, Restore as new.
 */
export function BackupsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const backups = useBackups(ref);
  const canDeploy = useCanDeploy(ref);
  // Backups are the platform's: operators set schedules (ADR 0027).
  const operator = useIsOperator();
  const run = useRunBackup();
  const running = backups.runs.some((r) => r.status === "running");
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const [restored, setRestored] = useState<string | null>(null);

  // Why Run now is off, said beside it.
  const why = !canDeploy
    ? "needs deploy access"
    : running
      ? "a backup or restore is running"
      : undefined;
  return (
    <section aria-labelledby="backups" className="flex flex-col gap-2.5">
      <SectionHead
        id="backups"
        right={
          <span className="flex items-center gap-2">
            <Act
              small
              cli={`dock backup run ${flags}`}
              disabled={!!why || run.isPending}
              reason={why}
              onClick={() => run.mutate(ref)}
            >
              Run now
            </Act>
            <ErrorText error={run.error} />
          </span>
        }
      >
        Backups
      </SectionHead>
      <dl className="props grid-cols-[120px_minmax(0,1fr)]">
        <Item label="How">
          {backupHow[backups.kind]}
          {backups.warning && (
            <span className="block text-warn">{backups.warning}</span>
          )}
        </Item>
        <Item label="Where">
          {backups.off_box ? (
            <span>off-box, to the configured repository</span>
          ) : (
            <span title="dock backup configure">
              <span className="text-warn">local, not off-box</span>
              {" · "}
              {operator ? (
                <Link to="/console/backups">Configure a repository</Link>
              ) : (
                <span className="text-mut">
                  an operator can configure a repository
                </span>
              )}
            </span>
          )}
        </Item>
        <Item label="Schedule">
          <Schedule serviceRef={ref} backups={backups} disabled={!operator} />
        </Item>
      </dl>
      {restored && (
        <RestoringInto
          serviceRef={ref}
          service={restored}
          kind={backups.kind}
        />
      )}
      <Runs
        serviceRef={ref}
        backups={backups}
        canRestore={canDeploy && !running}
        canDeploy={canDeploy}
        onRestored={setRestored}
      />
      {backups.restorable && backups.runs.length > 0 && (
        <p className="text-mut">
          A restore goes into a new service: check it, then point references at{" "}
          <code className="font-mono">
            {"${{"}
            {ref.service}-restored.{exportOf(backups.kind)}
            {"}}"}
          </code>{" "}
          and redeploy. To replace {ref.service}'s own data, restore a volume
          snapshot in place below.
        </p>
      )}
    </section>
  );
}

/**
 * Where a restore puts the data and what to do next, true while it runs
 * and after: the new service, and the reference to point at once it runs.
 */
export function RestoringInto({
  serviceRef: ref,
  service,
  kind,
}: {
  serviceRef: ServiceRef;
  service: string;
  kind: ServiceBackups["kind"];
}) {
  const org = useOrg();
  return (
    <p>
      The data goes into{" "}
      <Link
        to="/$org/$project/$environment/$service"
        params={{ org, ...ref, service }}
        className="font-mono"
      >
        {service}
      </Link>
      , a new service. Once it shows running, point references at{" "}
      <code className="font-mono">
        {"${{"}
        {service}.{exportOf(kind)}
        {"}}"}
      </code>{" "}
      and redeploy them. Until then nothing uses it.
    </p>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

/** Daily on or off, the hour, and when the next one runs. */
function Schedule({
  serviceRef: ref,
  backups: { schedule },
  disabled,
}: {
  serviceRef: ServiceRef;
  backups: ServiceBackups;
  disabled: boolean;
}) {
  const update = useBackupSchedule();
  const busy = disabled || update.isPending;
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
      <ErrorText error={update.error} />
    </span>
  );
}

const runStates: Record<BackupRun["status"], State> = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  succeeded: { tone: "ok", glyph: "●", label: "succeeded" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
};

/** The latest backups and restores, newest first by start. */
function Runs({
  serviceRef: ref,
  backups,
  canRestore,
  canDeploy,
  onRestored,
}: {
  serviceRef: ServiceRef;
  backups: ServiceBackups;
  canRestore: boolean;
  canDeploy: boolean;
  onRestored: (service: string) => void;
}) {
  const [restoring, setRestoring] = useState<number | null>(null);
  const [log, setLog] = useState<BackupRun | null>(null);
  const runs = backups.runs.toSorted(
    (a, b) => Date.parse(b.started_at) - Date.parse(a.started_at),
  );
  if (runs.length === 0) {
    return <p className="py-2 text-mut">No backups yet.</p>;
  }
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Started</th>
            <th>Run</th>
            <th>Status</th>
            <th>Size</th>
            <th>Snapshot</th>
            <th>Repository</th>
            <th>Detail</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <RunRow
              key={r.id}
              serviceRef={ref}
              run={r}
              onLog={canDeploy ? () => setLog(r) : undefined}
              restorable={
                backups.restorable &&
                r.operation === "backup" &&
                r.status === "succeeded" &&
                !r.pruned &&
                !!r.snapshot
              }
              canRestore={canRestore}
              open={restoring === r.id}
              onOpen={(open) => setRestoring(open ? r.id : null)}
              onRestored={(service) => {
                setRestoring(null);
                onRestored(service);
              }}
            />
          ))}
        </tbody>
      </table>
      {log && (
        <RunLog serviceRef={ref} run={log} onClose={() => setLog(null)} />
      )}
    </Panel>
  );
}

/** What a run's job printed, in a window (`dock backup log`). */
function RunLog({
  serviceRef: ref,
  run,
  onClose,
}: {
  serviceRef: ServiceRef;
  run: BackupRun;
  onClose: () => void;
}) {
  const log = useQuery(backupLogQuery(ref, run.id));
  const what = run.operation === "restore" ? "Restore" : "Backup";
  return (
    <Dialog
      title={`${what} ${run.id}, ${day(run.started_at)} ${clock(run.started_at)}`}
      onClose={onClose}
      wide
    >
      <div className="flex flex-col gap-3">
        {run.error && <p className="text-fail">{run.error}</p>}
        {log.isPending ? (
          <p className="text-mut">Loading the log.</p>
        ) : log.error ? (
          <ErrorText error={log.error} />
        ) : log.data.output ? (
          <pre className="max-h-[60vh] overflow-auto rounded-ctl border border-line bg-side p-3 font-mono text-xs whitespace-pre-wrap">
            {log.data.output}
          </pre>
        ) : (
          <p className="text-mut">
            No log was kept for this run: its job never ran, or it is older than
            the newest 20 runs.
          </p>
        )}
        <span className="font-mono text-2xs text-mut">
          dock backup log {run.id} -s {ref.service} -e {ref.environment}
        </span>
      </div>
    </Dialog>
  );
}

/** An empty cell's hyphen, so a missing value reads as missing. */
const none = <span className="text-faint">-</span>;

function RunRow({
  serviceRef: ref,
  run: r,
  onLog,
  restorable,
  canRestore,
  open,
  onOpen,
  onRestored,
}: {
  serviceRef: ServiceRef;
  run: BackupRun;
  /** Opens the run's log; unset without deploy access. */
  onLog?: () => void;
  restorable: boolean;
  canRestore: boolean;
  open: boolean;
  onOpen: (open: boolean) => void;
  onRestored: (service: string) => void;
}) {
  const org = useOrg();
  const restore = r.operation === "restore";
  return (
    <tr>
      <td title={`${day(r.started_at)} ${clock(r.started_at)}`}>
        {ago(r.started_at)}
      </td>
      <td>
        {restore
          ? "restore"
          : r.trigger === "schedule"
            ? "scheduled"
            : "manual"}
        {r.created_by !== "system" && (
          <span className="text-mut"> by {r.created_by}</span>
        )}
      </td>
      <td>
        <StateText state={runStates[r.status]} />
      </td>
      <td className="font-mono">
        {r.size_bytes > 0 ? size(r.size_bytes) : none}
      </td>
      <td className="font-mono">
        {r.snapshot ? (
          <span className={r.pruned ? "text-mut line-through" : undefined}>
            {r.snapshot.slice(0, 8)}
          </span>
        ) : (
          none
        )}
      </td>
      <td className={r.off_box ? "font-mono" : "font-mono text-warn"}>
        {r.repository}
      </td>
      <td className="max-w-[420px] whitespace-normal">
        {restore && r.restored_to && (
          <>
            into{" "}
            <Link
              to="/$org/$project/$environment/$service"
              params={{ org, ...ref, service: r.restored_to }}
              className="font-mono"
            >
              {r.restored_to}
            </Link>{" "}
          </>
        )}
        {r.pruned && <span className="text-mut">removed by retention </span>}
        {r.error && <span className="text-fail">{r.error}</span>}
        {restorable && r.not_restorable && (
          <span className="text-mut">
            Can't restore here: {r.not_restorable}
          </span>
        )}
        {!(restore && r.restored_to) &&
          !r.pruned &&
          !r.error &&
          !(restorable && r.not_restorable) &&
          none}
      </td>
      <td>
        <span className="flex items-center justify-end gap-1.5">
          {onLog && r.status !== "running" && (
            <Button small onClick={onLog}>
              View log
            </Button>
          )}
          {restorable && (
            <Button
              small
              disabled={!canRestore || !!r.not_restorable}
              reason={
                r.not_restorable
                  ? "see detail"
                  : canRestore
                    ? undefined
                    : "needs deploy access, and nothing running"
              }
              onClick={() => onOpen(true)}
            >
              Restore as new
            </Button>
          )}
        </span>
        {open && r.snapshot && (
          <RestoreConfirm
            serviceRef={ref}
            snapshot={r.snapshot}
            onCancel={() => onOpen(false)}
            onRestored={onRestored}
          />
        )}
      </td>
    </tr>
  );
}

/** Asks for the new service's name and starts the restore into it. */
function RestoreConfirm({
  serviceRef: ref,
  snapshot,
  onCancel,
  onRestored,
}: {
  serviceRef: ServiceRef;
  snapshot: string;
  onCancel: () => void;
  onRestored: (service: string) => void;
}) {
  const restore = useRestoreBackup();
  const [name, setName] = useState(`${ref.service}-restored`);
  const as = name.trim();
  return (
    <Confirm
      title={`Restore ${snapshot.slice(0, 8)} as a new service?`}
      action="Restore"
      cli={`dock backup restore ${ref.service} --snapshot ${snapshot.slice(0, 8)} --as ${as || "<name>"} -e ${ref.environment}`}
      pending={restore.isPending || !as}
      error={restore.error}
      onConfirm={() =>
        restore.mutate(
          { ref, snapshot, as },
          { onSuccess: (res) => onRestored(res.service.name) },
        )
      }
      onCancel={onCancel}
    >
      <p>
        The backup is restored into a new service. The live {ref.service} is not
        touched.
      </p>
      <label className="mt-3 flex flex-col gap-1.5 text-xs text-mut">
        New service
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="field font-mono"
          autoComplete="off"
          spellCheck={false}
        />
      </label>
    </Confirm>
  );
}
