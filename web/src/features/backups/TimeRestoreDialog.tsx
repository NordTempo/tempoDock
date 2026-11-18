import { useState } from "react";
import { useRestoreBackup } from "../../data/mutations";
import { useEnvironment } from "../../data/queries";
import type { BackupRun, ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { clock, day } from "../../ui/format";
import { Button, DialogFoot, ErrorText } from "../../ui/kit";
import {
  autofocus,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "../create/form";
import { PitrStrip } from "./PitrStrip";
import {
  baseFor,
  fromInputs,
  gapAt,
  moment,
  type PITR,
  restores,
  rfc3339,
  shortcuts,
  stretch,
  type stripMarks,
  toInputs,
  type Window,
  zoneLabel,
} from "./pitr";
import { restoredName } from "./points";

/**
 * Restores a Postgres database as it was at a moment (ADR 0093, mock A3):
 * the window as a small strip, the moment as a local date and time (sent
 * as UTC), shortcuts to just before a deploy and to the latest moment
 * (which the server resolves: it knows where the archive ends, for a
 * stopped database too), and the choice of a new
 * service (the default) or in place, which stops the database, keeps a
 * safety snapshot and asks for its name. Says which base backup it starts
 * from and how long a stretch of changes replays. A moment between two
 * ranges of the window (archiving was off) is refused here; the server
 * checks the archive reaches the moment and refuses with what it can.
 * Loaded when it opens.
 */
export default function TimeRestoreDialog({
  serviceRef: ref,
  pitr,
  runs,
  marks,
  window: w,
  initial,
  onClose,
  onStarted,
}: {
  serviceRef: ServiceRef;
  pitr: PITR;
  runs: BackupRun[];
  marks: ReturnType<typeof stripMarks>;
  window: Window;
  /** The moment picked on the tab's strip, if one was. */
  initial?: number;
  onClose: () => void;
  onStarted: () => void;
}) {
  const db = ref.service;
  const now = marks.span.end;
  const taken = useEnvironment(ref).services.map((s) => s.service.name);
  const options = shortcuts(w, pitr.deploys, now);
  const [when, setWhen] = useState(() =>
    // Just before the newest deploy, the usual accident; else the latest.
    toInputs(initial ?? options.find((o) => o.deploy)?.at ?? w.through),
  );
  // The latest shortcut, until another moment is picked or typed.
  const [latest, setLatest] = useState(false);
  const [inPlace, setInPlace] = useState(false);
  const [name, setName] = useState(() => restoredName(db, taken));
  const [typed, setTyped] = useState("");
  const restore = useRestoreBackup();
  const v = useValidation();

  const t = fromInputs(when.date, when.time);
  const valid = Number.isFinite(t);
  const outside = valid && !latest && !restores(w, t);
  const gap = valid && !latest ? gapAt(w, t) : undefined;
  const base = valid ? baseFor(runs, t) : undefined;
  const as = name.trim();
  const nameError = inPlace
    ? undefined
    : (nameProblem("service", as, taken) ??
      v.required(!as, "Enter a name for the new service."));
  const timeError = !valid
    ? v.required(true, "Enter a date and time.")
    : gap
      ? `Not archived from ${moment(gap.from, now)} to ${moment(gap.through, now)}: point in time was off or started over. Pick a moment before or after.`
      : outside
        ? `Pick a moment from ${moment(w.from, now)} to ${moment(w.through, now)}.`
        : undefined;
  const confirmed = !inPlace || typed.trim() === db;
  const users = pitr.used_by ?? [];
  const pick = (ms: number, isLatest = false) => {
    setLatest(isLatest);
    setWhen(toInputs(ms));
  };
  const toTime = latest ? "latest" : valid ? rfc3339(t) : "<time>";
  const target = latest ? { to_latest: true } : { to_time: rfc3339(t) };
  const cli = `dock backup restore ${db} --to-time ${toTime} ${inPlace ? "--in-place" : `--as ${as || "<name>"}`} -e ${ref.environment}`;

  const submit = () => {
    if (!v.check(valid && !outside && (inPlace || (!!as && !nameError))))
      return;
    if (!confirmed) return;
    restore.mutate(
      inPlace ? { ref, ...target, in_place: true } : { ref, ...target, as },
      { onSuccess: onStarted },
    );
  };

  const note = restore.error ? (
    <ErrorText error={restore.error} />
  ) : v.tried && (timeError || nameError) ? (
    <span className="text-fail">Fix the marked fields first.</span>
  ) : inPlace && !confirmed ? (
    <span className="text-faint">Type {db} to confirm.</span>
  ) : null;

  return (
    <Dialog
      wide
      title={
        inPlace && valid
          ? `Restore ${db} in place to ${moment(t, now)}?`
          : `Restore ${db} to a moment?`
      }
      onClose={onClose}
      className={`m-auto max-w-[640px] ${inPlace ? "border-fail-edge" : ""}`}
    >
      <form
        ref={v.form}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <div className="pt-[24px]">
          <PitrStrip
            compact
            marks={marks}
            window={w}
            value={valid ? t : undefined}
            onPick={(ms) => pick(ms)}
            label={`Moment to restore ${db} to`}
          />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            ref={autofocus}
            type="date"
            aria-label="Date"
            aria-invalid={!!timeError}
            value={when.date}
            onChange={(e) => {
              setLatest(false);
              setWhen({ ...when, date: e.target.value });
            }}
            className={`${input} ${invalid(timeError)}`}
          />
          <input
            type="time"
            step={1}
            aria-label="Time"
            aria-invalid={!!timeError}
            value={when.time}
            onChange={(e) => {
              setLatest(false);
              setWhen({ ...when, time: e.target.value });
            }}
            className={`${input} ${invalid(timeError)}`}
          />
          <span className="text-xs text-mut">{zoneLabel(valid ? t : now)}</span>
        </div>
        {timeError && (
          <span role="alert" className="-mt-1.5 text-xs text-fail">
            {timeError}
          </span>
        )}
        <fieldset className="seg -ml-[9px]" aria-label="Shortcuts">
          {options.map((o) => (
            <button
              key={o.label}
              type="button"
              aria-pressed={
                o.latest
                  ? latest
                  : !latest && valid && Math.abs(t - o.at) < 1000
              }
              onClick={() => pick(o.at, o.latest)}
            >
              {o.label}
            </button>
          ))}
        </fieldset>

        <fieldset className="flex flex-col gap-2" aria-label="Restore into">
          <label
            className={`flex cursor-pointer items-start gap-2.5 rounded-ctl border px-3 py-2.5 has-focus-visible:outline-2 has-focus-visible:outline-info ${inPlace ? "border-line" : "border-edge bg-hover"}`}
          >
            <input
              type="radio"
              name="into"
              checked={!inPlace}
              onChange={() => setInPlace(false)}
              className="mt-[5px] accent-fg"
            />
            <span className="flex min-w-0 flex-1 flex-col gap-1.5">
              <span className="text-fg">Into a new service</span>
              {!inPlace && (
                <>
                  <input
                    aria-label="New service"
                    aria-invalid={!!nameError}
                    value={name}
                    onChange={(e) => setName(toName(e.target.value))}
                    className={`${input} max-w-[280px] ${invalid(nameError)}`}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {nameError && (
                    <span role="alert" className="text-xs text-fail">
                      {nameError}
                    </span>
                  )}
                  <span className="text-xs text-mut">
                    {db} is not touched. {as || "The new service"} gets {db}'s
                    credentials; Use {as || "it"} switches services over once it
                    checks out.
                  </span>
                </>
              )}
            </span>
          </label>
          <label
            className={`flex cursor-pointer items-start gap-2.5 rounded-ctl border px-3 py-2.5 has-focus-visible:outline-2 has-focus-visible:outline-info ${inPlace ? "border-fail-edge bg-fail-bg" : "border-line"}`}
          >
            <input
              type="radio"
              name="into"
              checked={inPlace}
              onChange={() => setInPlace(true)}
              className="mt-[5px] accent-fail"
            />
            <span className="flex min-w-0 flex-1 flex-col gap-1">
              <span>
                <span className="text-fg">In place</span>{" "}
                <span className="text-xs text-fail">
                  {db} stops and its data is replaced
                </span>
              </span>
              {inPlace && valid && (
                <span className="text-xs text-sec">
                  Writes to {db} after {moment(t, now)} are replaced. The safety
                  snapshot is the way back.
                </span>
              )}
            </span>
          </label>
        </fieldset>

        {inPlace ? (
          <ol className="flex list-decimal flex-col gap-1 pl-5 text-xs text-sec">
            <li>
              <b className="font-semibold text-fg">{db} stops.</b>{" "}
              {users.length > 0
                ? `${list(users)} get connection errors until it starts again.`
                : "Anything using it gets connection errors until it starts again."}
            </li>
            <li>
              A safety snapshot keeps what {db} holds now, on its server
              (restoring it undoes this).
            </li>
            <li>
              <Plan base={base} at={valid ? t : undefined} now={now} />
            </li>
            <li>{db} starts on the restored data; nothing to switch.</li>
          </ol>
        ) : (
          <dl className="grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
            <dt className="text-mut">Starts from</dt>
            <dd className="text-sec">
              {base ? (
                <BaseName base={base} />
              ) : (
                "the newest base backup before it"
              )}
            </dd>
            <dt className="text-mut">Then replays</dt>
            <dd className="text-sec">
              {base?.consistent_at && valid
                ? `${stretch(t - Date.parse(base.consistent_at))} of changes`
                : "the changes up to the moment"}
            </dd>
          </dl>
        )}

        {inPlace && (
          <label className="flex flex-col gap-1.5 text-xs text-mut">
            <span>
              Type <b className="font-mono font-medium text-fg">{db}</b> to
              confirm
            </span>
            <input
              className="field font-mono"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setTyped(e.target.value)}
            />
          </label>
        )}

        <DialogFoot className="mt-1" cli={[cli]} note={note}>
          <Button onClick={onClose}>Cancel</Button>
          {inPlace ? (
            <Button
              type="submit"
              danger
              className="solid"
              disabled={restore.isPending || !confirmed}
            >
              Stop {db} and restore in place
            </Button>
          ) : (
            <button
              type="submit"
              className="btn pri"
              disabled={restore.isPending}
            >
              Restore into {as || "new service"} <kbd>↵</kbd>
            </button>
          )}
        </DialogFoot>
      </form>
    </Dialog>
  );
}

/** "base backup 7f2c91aa (Nov 17 03:00)", or with a capital B. */
function BaseName({ base, capital }: { base: BackupRun; capital?: boolean }) {
  const at = base.consistent_at ?? base.started_at;
  return (
    <>
      {capital ? "Base" : "base"} backup{" "}
      <span className="font-mono">{base.snapshot?.slice(0, 8) ?? base.id}</span>{" "}
      ({day(at)} {clock(at)})
    </>
  );
}

/** The in-place step that restores: which base backup, then to when. */
function Plan({
  base,
  at,
  now,
}: {
  base?: BackupRun;
  at?: number;
  now: number;
}) {
  const to = at === undefined ? "the moment" : moment(at, now);
  if (!base?.consistent_at || at === undefined)
    return (
      <>
        The newest base backup before it goes in, then changes replay to {to}.
      </>
    );
  return (
    <>
      <BaseName base={base} capital /> goes in, then{" "}
      {stretch(at - Date.parse(base.consistent_at))} of changes replay to {to}.
    </>
  );
}

/** "api", "api and worker", "api, worker and admin". */
const list = (items: readonly string[]) =>
  items.length < 2
    ? (items[0] ?? "")
    : `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`;
