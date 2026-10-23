import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  useBackupSchedule,
  useRestoreBackup,
  useRunBackup,
} from "../../data/mutations";
import { useBackups, useCanDeploy, useIsOperator } from "../../data/queries";
import type { BackupRun, ServiceBackups, ServiceRef } from "../../data/types";
import { ago, clock, day, size, until } from "../../ui/format";
import { Act, Button, Check, ErrorText, Panel, StateText } from "../../ui/kit";
import type { State } from "../../ui/status";
import { backupHow, exportOf, hourLabel } from "./backups";

/**
 * A stateful service's backups (ADR 0019): how and where they go, the
 * daily schedule, Run now, and the latest runs with Restore as new for
 * template databases.
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

  return (
    <>
      <dl className="props max-w-[960px] grid-cols-[120px_minmax(0,1fr)]">
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
            <span className="text-warn">
              local, not off-box.{" "}
              <span className="text-mut">
                An admin can set a repository with{" "}
                <code className="font-mono">dock backup configure</code>.
              </span>
            </span>
          )}
        </Item>
        <Item label="Schedule">
          <Schedule serviceRef={ref} backups={backups} disabled={!operator} />
        </Item>
      </dl>
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          cli={`dock backup run ${flags}`}
          disabled={!canDeploy || running || run.isPending}
          title={
            !canDeploy
              ? "Needs deploy access"
              : running
                ? "A backup or restore is running"
                : undefined
          }
          onClick={() => run.mutate(ref)}
        >
          Run now
        </Act>
        <ErrorText error={run.error} />
      </div>
      {restored && (
        <p>
          Restoring into{" "}
          <Link
            to="/$project/$environment/$service"
            params={{ ...ref, service: restored }}
            className="font-mono"
          >
            {restored}
          </Link>{" "}
          in the background. Nothing uses it until references point at{" "}
          <code className="font-mono">
            {"${{"}
            {restored}.{exportOf(backups.kind)}
            {"}}"}
          </code>
          .
        </p>
      )}
      <Runs
        serviceRef={ref}
        backups={backups}
        canRestore={canDeploy && !running}
        onRestored={setRestored}
      />
    </>
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
              className="field py-0.5 font-mono text-xs"
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
                  ? `${hourLabel(schedule.hour)}, the default`
                  : "the default hour"}
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

/** The latest backups and restores, newest first. */
function Runs({
  serviceRef: ref,
  backups,
  canRestore,
  onRestored,
}: {
  serviceRef: ServiceRef;
  backups: ServiceBackups;
  canRestore: boolean;
  onRestored: (service: string) => void;
}) {
  const [restoring, setRestoring] = useState<number | null>(null);
  if (backups.runs.length === 0) {
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
          {backups.runs.map((r) => (
            <RunRows
              key={r.id}
              serviceRef={ref}
              run={r}
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
    </Panel>
  );
}

function RunRows({
  serviceRef: ref,
  run: r,
  restorable,
  canRestore,
  open,
  onOpen,
  onRestored,
}: {
  serviceRef: ServiceRef;
  run: BackupRun;
  restorable: boolean;
  canRestore: boolean;
  open: boolean;
  onOpen: (open: boolean) => void;
  onRestored: (service: string) => void;
}) {
  const restore = r.operation === "restore";
  return (
    <>
      <tr>
        <td className="font-mono" title={ago(r.started_at)}>
          {day(r.started_at)} {clock(r.started_at)}
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
          {r.size_bytes > 0 ? size(r.size_bytes) : ""}
        </td>
        <td className="font-mono">
          {r.snapshot ? (
            <span className={r.pruned ? "text-mut line-through" : undefined}>
              {r.snapshot.slice(0, 8)}
            </span>
          ) : (
            ""
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
                to="/$project/$environment/$service"
                params={{ ...ref, service: r.restored_to }}
                className="font-mono"
              >
                {r.restored_to}
              </Link>{" "}
            </>
          )}
          {r.pruned && <span className="text-mut">removed by retention </span>}
          {r.error && <span className="text-fail">{r.error}</span>}
        </td>
        <td>
          {restorable && !open && (
            <Button
              small
              disabled={!canRestore}
              title={
                canRestore
                  ? undefined
                  : "Needs deploy access, and nothing running"
              }
              onClick={() => onOpen(true)}
            >
              Restore as new
            </Button>
          )}
        </td>
      </tr>
      {open && r.snapshot && (
        <tr>
          <td colSpan={8}>
            <RestoreForm
              serviceRef={ref}
              snapshot={r.snapshot}
              onCancel={() => onOpen(false)}
              onRestored={onRestored}
            />
          </td>
        </tr>
      )}
    </>
  );
}

/** Names the new service and starts the restore. */
function RestoreForm({
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
  return (
    <form
      aria-label="Restore as new service"
      className="flex flex-wrap items-center gap-2.5 whitespace-normal"
      onSubmit={(e) => {
        e.preventDefault();
        restore.mutate(
          { ref, snapshot, as: name.trim() },
          { onSuccess: (res) => onRestored(res.service.name) },
        );
      }}
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
    >
      <label htmlFor={`restore-${snapshot}`} className="text-mut">
        Restore <span className="font-mono">{snapshot.slice(0, 8)}</span> into a
        new service
      </label>
      <input
        id={`restore-${snapshot}`}
        // biome-ignore lint/a11y/noAutofocus: the form opens on request
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="field py-[5px] font-mono"
      />
      <Act
        primary
        small
        type="submit"
        cli={`dock backup restore ${ref.service} --snapshot ${snapshot.slice(0, 8)} --as ${name.trim() || "<name>"} -e ${ref.environment}`}
        disabled={restore.isPending || !name.trim()}
      >
        Restore
      </Act>
      <Button small onClick={onCancel}>
        Cancel
      </Button>
      <span className="text-mut">The live {ref.service} is not touched.</span>
      <ErrorText error={restore.error} />
    </form>
  );
}
