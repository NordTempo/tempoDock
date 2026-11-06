import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useBackups, useCanDeploy } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, clock, day, plural, size } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import type { State } from "../../ui/status";
import { RestoringInto } from "./BackupsPanel";
import {
  type Snapshot,
  type SnapshotRestore,
  useDeleteSnapshot,
  useRestoreSnapshot,
  useSnapshots,
  useTakeSnapshot,
} from "./data";

// A stateful service's volume snapshots (ADR 0045), under its backups:
// take one now, restore one into a new service or in place, delete one,
// and the latest restores.

const why: Record<Snapshot["trigger"], string> = {
  manual: "manual",
  before_restore: "before restore",
  before_deploy: "before deploy",
  before_run: "before run",
  before_move: "before move",
};

const snapshotStates: Record<Snapshot["status"], State> = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  ready: { tone: "ok", glyph: "●", label: "ready" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
};

/** Milliseconds as "1.8 s". */
const seconds = (ms: number) => `${(ms / 1000).toFixed(1)} s`;

/** What a row asks before acting on its snapshot. */
type Ask = { id: number; what: "new" | "in_place" | "delete" };

export function SnapshotsPanel({
  serviceRef: ref,
}: {
  serviceRef: ServiceRef;
}) {
  const { kind } = useBackups(ref);
  const snaps = useSnapshots(ref);
  const canDeploy = useCanDeploy(ref);
  const take = useTakeSnapshot();
  const [note, setNote] = useState("");
  const [ask, setAsk] = useState<Ask | null>(null);
  const [restored, setRestored] = useState<string | null>(null);
  const busy =
    snaps.snapshots.some((s) => s.status === "running") ||
    snaps.restores.some((r) => r.status === "running");
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const server = snaps.snapshots[0]?.server;

  return (
    <section aria-labelledby="snapshots" className="flex flex-col gap-2.5">
      <SectionHead
        id="snapshots"
        right={
          <form
            aria-label="Take snapshot"
            className="flex flex-wrap items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              take.mutate(
                { ref, note: note.trim() },
                { onSuccess: () => setNote("") },
              );
            }}
          >
            <input
              aria-label="Note"
              placeholder="note, optional"
              maxLength={200}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              className="field w-56 py-[3px] text-fg"
            />
            <Act
              small
              type="submit"
              cli={`dock volume snapshot ${flags}${note.trim() ? ` --note ${JSON.stringify(note.trim())}` : ""}`}
              disabled={!canDeploy || busy || take.isPending}
              reason={
                !canDeploy
                  ? "needs deploy access"
                  : busy
                    ? "a snapshot or restore is running"
                    : undefined
              }
            >
              Take snapshot
            </Act>
            <ErrorText error={take.error} />
          </form>
        }
      >
        Snapshots
      </SectionHead>
      <dl className="props grid-cols-[120px_minmax(0,1fr)]">
        <dt>How</dt>
        <dd>
          volume copies{server ? ` on ${server}` : ""}, the service paused while
          copied
        </dd>
        <dt>Keeps</dt>
        <dd>
          {plural(snaps.keep, "snapshot")}, {plural(snaps.keep_days, "day")}
        </dd>
      </dl>
      {restored && (
        <RestoringInto serviceRef={ref} service={restored} kind={kind} />
      )}
      {snaps.snapshots.length === 0 ? (
        <p className="text-mut">No snapshots yet.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>Taken</th>
                <th>Why</th>
                <th>Status</th>
                <th>Size</th>
                <th>Paused</th>
                <th className="w-full">Note</th>
                <th className="text-right">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {snaps.snapshots.map((s) => (
                <SnapshotRow
                  key={s.id}
                  serviceRef={ref}
                  snapshot={s}
                  intoNew={snaps.into_new}
                  canAct={canDeploy && !busy}
                  ask={ask?.id === s.id ? ask.what : null}
                  onAsk={(what) => setAsk(what ? { id: s.id, what } : null)}
                  onRestored={setRestored}
                  onRetry={() => take.mutate({ ref, note: s.note ?? "" })}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      {snaps.restores.slice(0, 3).map((r) => (
        <RestoreLine key={r.id} serviceRef={ref} restore={r} />
      ))}
    </section>
  );
}

/** An empty cell's hyphen, so a missing value reads as missing. */
const none = <span className="text-faint">-</span>;

/** One snapshot, its actions, and the confirmation one of them opened. */
function SnapshotRow({
  serviceRef: ref,
  snapshot: s,
  intoNew,
  canAct,
  ask,
  onAsk,
  onRestored,
  onRetry,
}: {
  serviceRef: ServiceRef;
  snapshot: Snapshot;
  intoNew: boolean;
  canAct: boolean;
  ask: Ask["what"] | null;
  onAsk: (what: Ask["what"] | null) => void;
  onRestored: (service: string) => void;
  /** Takes a new snapshot with a failed one's note. */
  onRetry: () => void;
}) {
  const restore = useRestoreSnapshot();
  const remove = useDeleteSnapshot();
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const reason = canAct
    ? undefined
    : "needs deploy access, and nothing running";
  const cancel = (reset: () => void) => () => {
    reset();
    onAsk(null);
  };
  const ready = s.status === "ready";
  return (
    <tr>
      <td className="font-mono">{s.id}</td>
      <td title={`${day(s.started_at)} ${clock(s.started_at)}`}>
        {ago(s.started_at)}
      </td>
      <td>
        {why[s.trigger]}
        {s.created_by !== "system" && (
          <span className="text-mut"> by {s.created_by}</span>
        )}
      </td>
      <td>
        <StateText state={snapshotStates[s.status]} />
      </td>
      <td className="font-mono">{ready ? size(s.size_bytes) : none}</td>
      <td className="font-mono">{ready ? seconds(s.paused_ms) : none}</td>
      <td className="max-w-[420px] whitespace-normal">
        {s.error ? (
          <span className="text-fail">{s.error}</span>
        ) : (
          s.note || none
        )}
      </td>
      {/* Actions sit at the right edge, whichever a row has. */}
      <td className="whitespace-nowrap">
        {s.status !== "running" && (
          <span className="flex justify-end gap-1.5">
            {s.status === "failed" && (
              <Button
                small
                disabled={!canAct}
                reason={reason}
                title={`dock volume snapshot ${flags}`}
                onClick={onRetry}
              >
                Retry
              </Button>
            )}
            {ready && intoNew && (
              <Button
                small
                disabled={!canAct}
                reason={reason}
                onClick={() => onAsk("new")}
              >
                Restore as new
              </Button>
            )}
            {ready && (
              <Button
                small
                disabled={!canAct}
                reason={reason}
                onClick={() => onAsk("in_place")}
              >
                In place
              </Button>
            )}
            <Button
              small
              disabled={!canAct}
              reason={reason}
              onClick={() => onAsk("delete")}
            >
              Delete
            </Button>
          </span>
        )}
        {ask === "new" && (
          <RestoreConfirm
            serviceRef={ref}
            id={s.id}
            onCancel={() => onAsk(null)}
            onRestored={(service) => {
              onAsk(null);
              onRestored(service);
            }}
          />
        )}
        {ask === "in_place" && (
          <Confirm
            danger
            title={`Replace ${ref.service}'s data with snapshot ${s.id}?`}
            action={`Restore snapshot ${s.id}`}
            cli={`dock volume snapshot restore ${s.id} ${flags} --in-place`}
            pending={restore.isPending}
            error={restore.error}
            onConfirm={() =>
              restore.mutate(
                { ref, id: s.id, in_place: true },
                { onSuccess: () => onAsk(null) },
              )
            }
            onCancel={cancel(restore.reset)}
          >
            The service stops while it is copied in. Its current data is
            snapshotted first, and restoring that snapshot undoes this.
          </Confirm>
        )}
        {ask === "delete" && (
          <Confirm
            danger
            action={`Delete snapshot ${s.id}`}
            cli={`dock volume snapshot delete ${s.id} ${flags}`}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate({ ref, id: s.id }, { onSuccess: () => onAsk(null) })
            }
            onCancel={cancel(remove.reset)}
          >
            Its files on the server are removed. This can't be undone.
          </Confirm>
        )}
      </td>
    </tr>
  );
}

/** Asks for the new service's name and restores the snapshot into it. */
function RestoreConfirm({
  serviceRef: ref,
  id,
  onCancel,
  onRestored,
}: {
  serviceRef: ServiceRef;
  id: number;
  onCancel: () => void;
  onRestored: (service: string) => void;
}) {
  const restore = useRestoreSnapshot();
  const fallback = `${ref.service}-snapshot-${id}`;
  const [name, setName] = useState(fallback);
  const as = name.trim();
  return (
    <Confirm
      title={`Restore snapshot ${id} as a new service?`}
      action="Restore"
      cli={`dock volume snapshot restore ${id} -s ${ref.service} -e ${ref.environment}${as && as !== fallback ? ` --as ${as}` : ""}`}
      pending={restore.isPending || !as}
      error={restore.error}
      onConfirm={() =>
        restore.mutate(
          { ref, id, as },
          { onSuccess: (res) => onRestored(res.service?.name ?? as) },
        )
      }
      onCancel={onCancel}
    >
      <p>
        The snapshot is restored into a new service. The live {ref.service} is
        not touched.
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

/** One restore in words: where it went, how it went, and how to undo it. */
function RestoreLine({
  serviceRef: ref,
  restore: r,
}: {
  serviceRef: ServiceRef;
  restore: SnapshotRestore;
}) {
  const org = useOrg();
  return (
    <p className="text-mut">
      <span className="font-mono">
        {r.snapshot ? `#${r.snapshot}` : "a deleted snapshot"}
      </span>{" "}
      {r.in_place ? (
        "in place"
      ) : (
        <>
          into{" "}
          <Link
            to="/$org/$project/$environment/$service"
            params={{ org, ...ref, service: r.to }}
            className="font-mono"
          >
            {r.to}
          </Link>
        </>
      )}
      ,{" "}
      {r.status === "running" ? (
        "running"
      ) : r.status === "failed" ? (
        <span className="text-fail">failed: {r.error}</span>
      ) : (
        `done ${ago(r.finished_at ?? r.started_at)}`
      )}
      {r.safety ? ` (undo: snapshot ${r.safety})` : ""}
    </p>
  );
}
