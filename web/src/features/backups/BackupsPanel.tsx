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
        onRestored={setRestored}
      />
    </section>
  );
}

/** Says a restore into a new service runs, and how to start using it. */
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
      Restoring into{" "}
      <Link
        to="/$org/$project/$environment/$service"
        params={{ org, ...ref, service }}
        className="font-mono"
      >
        {service}
      </Link>{" "}
      in the background. Nothing uses it until references point at{" "}
      <code className="font-mono">
        {"${{"}
        {service}.{exportOf(kind)}
        {"}}"}
      </code>
      .
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
            <RunRow
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

/** An empty cell's hyphen, so a missing value reads as missing. */
const none = <span className="text-faint">-</span>;

function RunRow({
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
        {!(restore && r.restored_to) && !r.pruned && !r.error && none}
      </td>
      <td>
        {restorable && (
          <Button
            small
            disabled={!canRestore}
            reason={
              canRestore
                ? undefined
                : "needs deploy access, and nothing running"
            }
            onClick={() => onOpen(true)}
          >
            Restore as new
          </Button>
        )}
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
