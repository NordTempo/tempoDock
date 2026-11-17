import { useIsOperator } from "../../data/queries";
import type { BackupRun, ServiceRef } from "../../data/types";
import {
  ago,
  bytes,
  clock,
  clockSec,
  day,
  plural,
  since,
} from "../../ui/format";
import { Act, StateText } from "../../ui/kit";
import type { State } from "../../ui/status";
import type { PitrMode } from "./PitrDialog";
import { PitrStrip } from "./PitrStrip";
import {
  type Archiver,
  type ArchiveState,
  archiveState,
  moment,
  newestBase,
  type PITR,
  type stripMarks,
} from "./pitr";

/**
 * The Backups tab's "Point in time" settings row (ADR 0093): on with its
 * days and Edit, or off. Operators change it, like the schedule.
 */
export function PitrRow({
  serviceRef: ref,
  pitr,
  onSettings,
}: {
  serviceRef: ServiceRef;
  pitr: PITR;
  onSettings: (mode: PitrMode) => void;
}) {
  const operator = useIsOperator();
  if (!pitr.enabled)
    return <>Off: restores reach the daily dumps and snapshots only</>;
  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span>
        On, keeps {plural(pitr.days, "day")}: changes are archived off-site as
        they happen, a base backup joins each daily dump
      </span>
      {operator && (
        <Act
          small
          cli={`dock backup pitr on ${ref.service} --days N -e ${ref.environment}`}
          onClick={() => onSettings("edit")}
        >
          Edit
        </Act>
      )}
    </span>
  );
}

const archiveStates: Record<ArchiveState, State> = {
  ok: { tone: "ok", glyph: "●", label: "archiving" },
  behind: { tone: "warn", glyph: "▲", label: "behind" },
  failing: { tone: "fail", glyph: "✕", label: "failing" },
  unknown: { tone: "mut", glyph: "○", label: "archiver not answering" },
};

/**
 * A database's restorable moments over the restore points (mock A): on,
 * the window with "Restore to a time…" as the page's primary, how
 * archiving does (a box with what the archiver says while it is behind or
 * failing), and the strip, where a click opens the dialog at that moment.
 * Off, the strip shows the ticks that restore today and operators get
 * Turn on (or why it can't be).
 */
export function PitrSection({
  serviceRef: ref,
  pitr,
  runs,
  marks,
  window: w,
  why,
  onRestore,
  onSettings,
}: {
  serviceRef: ServiceRef;
  pitr: PITR;
  runs: BackupRun[];
  marks: ReturnType<typeof stripMarks>;
  window?: { from: number; through: number };
  /** Why restoring waits, when it does. */
  why?: string;
  onRestore: (at?: number) => void;
  onSettings: (mode: PitrMode) => void;
}) {
  const operator = useIsOperator();
  const db = ref.service;
  const now = marks.span.end;
  const state = archiveState(pitr);
  const base = newestBase(runs);
  const strip = (
    // Room above for the hover's moment, where it shows one.
    <div className={w ? "pt-[30px]" : "pt-2"}>
      <PitrStrip
        marks={marks}
        window={w}
        onPick={why ? undefined : (t) => onRestore(t)}
        label={`Moment to restore ${db} to`}
      />
    </div>
  );

  if (!pitr.enabled) {
    return (
      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h3 className="text-md font-semibold text-fg">Restorable moments</h3>
          {!pitr.unavailable && (
            <span className="ml-auto text-xs">
              {operator ? (
                <Act
                  small
                  cli={`dock backup pitr on ${db} --days ${pitr.days} -e ${ref.environment}`}
                  onClick={() => onSettings("on")}
                >
                  Turn on point in time…
                </Act>
              ) : (
                <span className="text-mut">
                  An operator can turn on point in time
                </span>
              )}
            </span>
          )}
        </div>
        {pitr.unavailable && (
          <p className="text-xs text-mut">
            Point in time can't be turned on: <Code text={pitr.unavailable} />
          </p>
        )}
        {strip}
        <p className="text-xs text-mut">
          Only the ticks restore. Point in time makes every second between them
          restorable.
        </p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h3 className="text-md font-semibold text-fg">
          {w ? (
            <>
              Any moment from{" "}
              <span className="font-mono">{moment(w.from, now)}</span> to{" "}
              <span className="font-mono">{moment(w.through, now)}</span>
            </>
          ) : (
            "Point in time"
          )}
        </h3>
        {w && (
          <span className="ml-auto">
            <Act
              small
              primary={!why}
              cli={`dock backup restore ${db} --to-time TIME -e ${ref.environment}`}
              disabled={!!why}
              reason={why}
              onClick={() => onRestore()}
            >
              Restore to a time…
            </Act>
          </span>
        )}
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {/* Behind or failing, the box below says it once. */}
        {(state === "ok" || state === "unknown") && (
          <StateText state={archiveStates[state]} />
        )}
        {pitr.through && (
          <span>
            archived through{" "}
            <span className="font-mono text-fg">
              {moment(Date.parse(pitr.through), now)}
            </span>{" "}
            <span className="text-mut">({ago(pitr.through)})</span>
          </span>
        )}
        <span className="text-mut">
          {w
            ? plural(pitr.base_backups, "base backup")
            : "the window opens with the first base backup"}
          {base?.consistent_at &&
            `, newest ${day(base.consistent_at)} ${clock(base.consistent_at)}`}
        </span>
        {pitr.wal_bytes > 0 && (
          <span className="text-mut">
            WAL kept <span className="font-mono">{bytes(pitr.wal_bytes)}</span>
          </span>
        )}
      </div>
      {pitr.archiver && (state === "behind" || state === "failing") && (
        <ArchiverBox
          db={db}
          archiver={pitr.archiver}
          failing={state === "failing"}
        />
      )}
      {strip}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-mut">
        <span className="inline-flex items-center gap-1.5">
          <i aria-hidden className="inline-block h-[14px] w-[2px] bg-sec" />
          dump and base backup
        </span>
        <span className="inline-flex items-center gap-1.5">
          <i
            aria-hidden
            className="inline-block size-[8px] rotate-45 bg-info"
          />
          volume snapshot
        </span>
        <span className="inline-flex items-center gap-1.5">
          <i
            aria-hidden
            className="inline-block size-0 border-x-[5px] border-t-[7px] border-x-transparent border-t-mut"
          />
          deploy of a service using {db}
        </span>
        {w && !why && (
          <span className="ml-auto">
            Click the strip to restore to that moment
          </span>
        )}
      </div>
    </div>
  );
}

/**
 * What the archiver says while it is behind (amber) or failing (red):
 * what waits since when, the last error, and the disk the waiting WAL
 * fills. Announced politely as it changes.
 */
function ArchiverBox({
  db,
  archiver: a,
  failing,
}: {
  db: string;
  archiver: Archiver;
  failing: boolean;
}) {
  const oldest = a.oldest_waiting;
  return (
    <div
      aria-live="polite"
      className={`flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-panel border px-3.5 py-2.5 text-xs ${failing ? "border-fail-edge bg-fail-bg" : "border-warn-edge bg-stage-bg"}`}
    >
      <StateText
        state={failing ? archiveStates.failing : archiveStates.behind}
      />
      <span
        className={`min-w-0 flex-1 ${failing ? "text-fg" : "text-warn-text"}`}
      >
        {failing && a.last_error && (
          <span className="block">
            Archiving has failed
            {a.last_error_at && (
              <>
                {" "}
                since{" "}
                <span className="font-mono">{clockSec(a.last_error_at)}</span>
              </>
            )}
            : <span className="font-mono text-fail">{a.last_error}</span>
          </span>
        )}
        {a.waiting > 0 && (
          <span className="block">
            {plural(a.waiting, "segment")} ({bytes(a.waiting_bytes)}) wait to be
            archived
            {oldest && (
              <>
                , the oldest since{" "}
                <span className="font-mono">{clockSec(oldest)}</span> (
                {since(oldest)})
              </>
            )}
            . Writes after it are only on {db}'s server until they upload.
          </span>
        )}
        {/* The archiver's own words, when nothing above says why. */}
        {a.problem && !(failing && a.last_error) && a.waiting === 0 && (
          <span className="block">{a.problem}</span>
        )}
      </span>
      {a.disk_bytes && a.disk_free !== undefined ? (
        <span className="text-mut">
          {bytes(a.disk_free)} free of {bytes(a.disk_bytes)} on {db}'s disk
        </span>
      ) : null}
    </div>
  );
}

/** Text with `code` spans, as the server writes commands in reasons. */
function Code({ text }: { text: string }) {
  return (
    <>
      {text.split("`").map((part, i) =>
        i % 2 ? (
          <code key={i} className="font-mono text-fg">
            {part}
          </code>
        ) : (
          part
        ),
      )}
    </>
  );
}
