import { useQuery } from "@tanstack/react-query";
import { Fragment, useMemo, useState } from "react";
import { useCanDeploy, useDeployBlock, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { ago, clock, day } from "../../ui/format";
import {
  Act,
  Actor,
  actorKind,
  Confirm,
  ErrorText,
  Panel,
  StateText,
} from "../../ui/kit";
import { type CommandRun, isRunning, runQueries } from "../runs/data";
import { Output, quote, runStates, timeouts, took } from "../runs/RunPanel";
import { describeCron, knownZone, nextTimes, parseCron } from "./cron";
import {
  type Job,
  jobQueries,
  type SetJobRequest,
  useRemoveJob,
  useRunJob,
  useSetJob,
  useSetJobEnabled,
} from "./data";

/** Where the page's selection is: a job (and an opened run), or a new job. */
type Selection = { job: string; run: number | null } | "new" | null;

/**
 * A service's Jobs tab (ADR 0054): every job in a table with Run now,
 * and the selected one under it, its schedule editor over its history.
 * Job and run events keep both current; nothing polls.
 */
export function JobsPanel({
  serviceRef: ref,
  jobs,
}: {
  serviceRef: ServiceRef;
  jobs: Job[];
}) {
  const [picked, setPicked] = useState<Selection>(null);
  // The first job until one is picked; a removed job's selection falls back.
  const selection: Selection =
    picked === "new" || (picked && jobs.some((j) => j.name === picked.job))
      ? picked
      : jobs[0]
        ? { job: jobs[0].name, run: null }
        : null;
  const job =
    selection && selection !== "new"
      ? jobs.find((j) => j.name === selection.job)
      : undefined;
  const canDeploy = useCanDeploy(ref);
  const { service } = useService(ref);
  return (
    <>
      {jobs.length === 0 ? (
        <p className="text-mut">
          No jobs yet. Add one here, with <code>dock job set</code>, or under{" "}
          <code>[[jobs]]</code> in dock.toml.
        </p>
      ) : (
        <JobsTable
          serviceRef={ref}
          jobs={jobs}
          selected={job?.name}
          onSelect={(name, run) => setPicked({ job: name, run: run ?? null })}
        />
      )}
      {!service.active_release && jobs.length > 0 && (
        <p className="text-mut">
          Deploy first: jobs run in the active release's image, and are skipped
          until there is one.
        </p>
      )}
      {selection !== "new" && canDeploy && (
        <div>
          <Act
            cli={`dock job set NAME -s ${ref.service} -e ${ref.environment} --schedule '0 3 * * *' -- <command>`}
            onClick={() => setPicked("new")}
          >
            New job
          </Act>
        </div>
      )}
      {selection === "new" ? (
        <section aria-label="New job" className="flex flex-col gap-3">
          <h2 className="text-sm font-semibold">New job</h2>
          <Editor
            serviceRef={ref}
            onSaved={(name) => setPicked({ job: name, run: null })}
            onCancel={() => setPicked(null)}
          />
        </section>
      ) : (
        job &&
        selection && (
          <JobDetail
            key={job.name}
            serviceRef={ref}
            job={job}
            openRun={selection.run}
            onOpenRun={(run) => setPicked({ job: job.name, run })}
          />
        )
      )}
    </>
  );
}

/** "15:00" today, "Nov 5 03:00" on another day, in the viewer's time. */
const when = (iso: string) =>
  new Date(iso).toDateString() === new Date().toDateString()
    ? clock(iso)
    : `${day(iso)} ${clock(iso)}`;

/**
 * When a job runs next: the API's time, or counted here from now when
 * that has passed (a scheduled run started since the list was read).
 */
function nextOf(job: Job) {
  if (!job.enabled) return undefined;
  const now = Date.now();
  if (job.next_at && Date.parse(job.next_at) > now) return job.next_at;
  const parsed = parseCron(job.schedule);
  if ("error" in parsed || !knownZone(job.timezone)) return job.next_at;
  return nextTimes(parsed.schedule, job.timezone, now, 1)[0]?.toISOString();
}

/** `-s web -e production`: which service a CLI hint acts on. */
const target = (ref: ServiceRef) => `-s ${ref.service} -e ${ref.environment}`;

/** The jobs, by name; a name opens the job under the table. */
function JobsTable({
  serviceRef: ref,
  jobs,
  selected,
  onSelect,
}: {
  serviceRef: ServiceRef;
  jobs: Job[];
  selected: string | undefined;
  onSelect: (name: string, run?: number) => void;
}) {
  const canDeploy = useCanDeploy(ref);
  const blocked = useDeployBlock(ref);
  const { service } = useService(ref);
  const run = useRunJob();
  const enable = useSetJobEnabled();
  const runBlock = !canDeploy
    ? "Needs deploy access"
    : !service.active_release
      ? "Deploy first"
      : blocked;
  return (
    <div className="flex flex-col gap-1.5">
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>Schedule</th>
              <th>Next</th>
              <th>Last run</th>
              <th>Source</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {jobs.map((j) => {
              const open = j.name === selected;
              const words = describeCron(j.schedule);
              const next = nextOf(j);
              const last = j.last_run;
              const pending = (m: {
                isPending: boolean;
                variables?: { name: string };
              }) => m.isPending && m.variables?.name === j.name;
              return (
                <tr
                  key={j.name}
                  className={`${open ? "bg-sel" : ""} ${j.enabled ? "" : "text-faint"}`}
                >
                  <td>
                    <button
                      type="button"
                      aria-expanded={open}
                      className={`font-mono hover:text-fg ${open ? "text-fg" : ""}`}
                      onClick={() => onSelect(j.name)}
                    >
                      <span aria-hidden className="text-faint">
                        {open ? "▾" : "▸"}
                      </span>{" "}
                      {j.name}
                    </button>
                  </td>
                  <td>
                    {words && <span className="mr-2">{words}</span>}
                    <span className="font-mono text-faint">
                      {j.schedule} · {j.timezone}
                    </span>
                  </td>
                  <td className="font-mono" title={next}>
                    {j.enabled ? (next ? when(next) : "") : "off"}
                  </td>
                  <td>
                    {last && (
                      <>
                        <StateText state={runStates[last.status]} />{" "}
                        <span className="font-mono text-faint">
                          {last.exit_code ? `exit ${last.exit_code} · ` : ""}
                          {when(last.started_at)}
                        </span>
                      </>
                    )}
                  </td>
                  <td>{j.source === "repo" ? "dock.toml" : "api"}</td>
                  <td className="text-right">
                    <span className="inline-flex gap-1.5">
                      <Act
                        small
                        cli={`dock job ${j.enabled ? "disable" : "enable"} ${j.name} ${target(ref)}`}
                        disabled={!canDeploy || pending(enable)}
                        title={canDeploy ? undefined : "Needs deploy access"}
                        onClick={() =>
                          enable.mutate({
                            ref,
                            name: j.name,
                            enabled: !j.enabled,
                          })
                        }
                      >
                        {j.enabled ? "Disable" : "Enable"}
                      </Act>
                      <Act
                        small
                        cli={`dock job run ${j.name} ${target(ref)}`}
                        disabled={!!runBlock || pending(run)}
                        title={runBlock}
                        onClick={() =>
                          run.mutate(
                            { ref, name: j.name },
                            { onSuccess: (r) => onSelect(j.name, r.id) },
                          )
                        }
                      >
                        Run now
                      </Act>
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>
      <ErrorText error={run.error ?? enable.error} />
    </div>
  );
}

/** A job: who set it, Remove, its editor and its history. */
function JobDetail({
  serviceRef: ref,
  job,
  openRun,
  onOpenRun,
}: {
  serviceRef: ServiceRef;
  job: Job;
  openRun: number | null;
  onOpenRun: (id: number | null) => void;
}) {
  const canDeploy = useCanDeploy(ref);
  const remove = useRemoveJob();
  const [confirming, setConfirming] = useState(false);
  const cli = `dock job remove ${job.name} ${target(ref)}`;
  return (
    <section aria-label={`Job ${job.name}`} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h2 className="font-mono text-sm font-semibold">{job.name}</h2>
        <span className="text-xs text-faint">
          {job.source === "repo"
            ? "from dock.toml"
            : `set by ${job.created_by} · ${day(job.updated_at)}`}
        </span>
        {job.source === "api" && canDeploy && (
          <span className="ml-auto">
            <Act small cli={cli} onClick={() => setConfirming(true)}>
              Remove
            </Act>
          </span>
        )}
      </div>
      {confirming && (
        <Confirm
          action="Remove"
          danger
          cli={cli}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate({ ref, name: job.name })}
          onCancel={() => setConfirming(false)}
        >
          Remove <span className="font-mono">{job.name}</span> and its history?
        </Confirm>
      )}
      {/* A change from elsewhere starts the form over from the new job. */}
      <Editor key={job.updated_at} serviceRef={ref} job={job} />
      <History
        serviceRef={ref}
        job={job.name}
        open={openRun}
        onOpen={onOpenRun}
        canRead={canDeploy}
      />
    </section>
  );
}

/** Every IANA timezone the browser knows, read once the editor opens. */
let zones: string[] | undefined;
const allZones = () => {
  zones ??= [
    "UTC",
    ...Intl.supportedValuesOf("timeZone").filter((z) => z !== "UTC"),
  ];
  return zones;
};

/** The viewer's timezone, a new job's default. */
const localZone = () =>
  Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

/** Seconds as a Go duration for `--timeout`: "45m", "1h30m". */
function duration(seconds: number) {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return `${h ? `${h}h` : ""}${m ? `${m}m` : ""}${s || seconds === 0 ? `${s}s` : ""}`;
}

/** A preview time in the job's timezone: "Thu Nov 5 03:00". */
function zoned(at: Date, tz: string) {
  return at.toLocaleString("en", {
    timeZone: tz,
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
}

const DEFAULT_TIMEOUT = 3600;

/**
 * The schedule editor: name (new jobs only), expression and timezone with
 * the schedule in words and its next three times, command, timeout and
 * overlap. A job from dock.toml shows read-only: it changes in the file.
 */
function Editor({
  serviceRef: ref,
  job,
  onSaved,
  onCancel,
}: {
  serviceRef: ServiceRef;
  job?: Job;
  onSaved?: (name: string) => void;
  onCancel?: () => void;
}) {
  const canDeploy = useCanDeploy(ref);
  const set = useSetJob();
  const [name, setName] = useState(job?.name ?? "");
  const [schedule, setSchedule] = useState(job?.schedule ?? "");
  const [timezone, setTimezone] = useState(job?.timezone ?? localZone());
  const [command, setCommand] = useState(job?.command ?? "");
  const [timeout, setTimeoutSeconds] = useState(
    job?.timeout_seconds ?? DEFAULT_TIMEOUT,
  );
  const [overlap, setOverlap] = useState<Job["overlap"]>(
    job?.overlap ?? "skip",
  );

  const fromFile = job?.source === "repo";
  const readOnly = fromFile || !canDeploy;
  const tz = timezone.trim() || "UTC";
  const trimmed = command.trim();
  const preview = useMemo(() => {
    if (!schedule.trim()) return undefined;
    const parsed = parseCron(schedule);
    if ("error" in parsed) return { error: parsed.error };
    if (!knownZone(tz)) return { error: `unknown timezone ${tz}` };
    return {
      words: describeCron(schedule),
      times: nextTimes(parsed.schedule, tz, Date.now(), 3),
    };
  }, [schedule, tz]);
  const nameError =
    !job && name && !/^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/.test(name)
      ? "lower-case letters, digits and dashes, at most 40"
      : undefined;
  const changed =
    !job ||
    schedule !== job.schedule ||
    tz !== job.timezone ||
    trimmed !== job.command ||
    timeout !== job.timeout_seconds ||
    overlap !== job.overlap;
  const valid =
    !!name && !nameError && !!trimmed && !!preview && !("error" in preview);

  const offered: readonly (readonly [number, string])[] = timeouts.some(
    ([s]) => (s || DEFAULT_TIMEOUT) === timeout,
  )
    ? timeouts
    : [...timeouts, [timeout, duration(timeout)]];
  const cli = [
    `dock job set ${name || "NAME"} ${target(ref)}`,
    `--schedule ${schedule.trim() ? quote(schedule.trim()) : "'0 3 * * *'"}`,
    tz !== "UTC" && `--timezone ${tz}`,
    timeout !== DEFAULT_TIMEOUT && `--timeout ${duration(timeout)}`,
    overlap === "allow" && "--overlap allow",
    `-- ${trimmed ? quote(trimmed) : "<command>"}`,
  ]
    .filter(Boolean)
    .join(" ");

  const label = "text-right text-faint";
  return (
    <form
      aria-label={job ? `Schedule of ${job.name}` : "New job"}
      className="grid grid-cols-[100px_minmax(0,1fr)] items-center gap-x-3 gap-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (readOnly || !valid || !changed) return;
        const req: SetJobRequest = {
          schedule: schedule.trim(),
          timezone: tz,
          command: trimmed,
          timeout_seconds: timeout,
          overlap,
          // A replaced job keeps its on or off.
          ...(job && { enabled: job.enabled }),
        };
        set.mutate({ ref, name, req }, { onSuccess: () => onSaved?.(name) });
      }}
    >
      {!job && (
        <>
          <label htmlFor="job-name" className={label}>
            Name
          </label>
          <span className="flex flex-wrap items-center gap-2.5">
            <input
              id="job-name"
              placeholder="nightly-cleanup"
              spellCheck={false}
              autoComplete="off"
              maxLength={40}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="field w-56 font-mono"
            />
            {nameError && <span className="text-fail">{nameError}</span>}
          </span>
        </>
      )}
      <label htmlFor="job-schedule" className={label}>
        Schedule
      </label>
      <span className="flex flex-wrap items-center gap-2.5">
        <input
          id="job-schedule"
          placeholder="0 3 * * *"
          spellCheck={false}
          autoComplete="off"
          maxLength={200}
          readOnly={readOnly}
          value={schedule}
          onChange={(e) => setSchedule(e.target.value)}
          className="field w-44 font-mono"
        />
        <input
          aria-label="Timezone"
          list={readOnly ? undefined : "job-zones"}
          spellCheck={false}
          autoComplete="off"
          maxLength={64}
          readOnly={readOnly}
          value={timezone}
          onChange={(e) => setTimezone(e.target.value)}
          className="field w-44 font-mono"
        />
        {!readOnly && (
          <datalist id="job-zones">
            {allZones().map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
        )}
        {preview &&
          ("error" in preview ? (
            <span className="text-fail">{preview.error}</span>
          ) : (
            <>
              {preview.words && <span>{preview.words}</span>}
              <span className="font-mono text-faint">
                {preview.times.map((t) => zoned(t, tz)).join(" · ")}
              </span>
            </>
          ))}
      </span>
      <label htmlFor="job-command" className={label}>
        Command
      </label>
      <textarea
        id="job-command"
        placeholder="a command, e.g. node scripts/cleanup.js"
        spellCheck={false}
        autoComplete="off"
        maxLength={16_384}
        rows={trimmed.includes("\n") ? 4 : 1}
        readOnly={readOnly}
        value={command}
        onChange={(e) => setCommand(e.target.value)}
        className="field resize-y font-mono"
      />
      <label htmlFor="job-timeout" className={label}>
        Timeout
      </label>
      <span className="flex flex-wrap items-center gap-2.5">
        <select
          id="job-timeout"
          className="field py-0.5 font-mono text-xs"
          value={timeout}
          disabled={readOnly}
          onChange={(e) => setTimeoutSeconds(Number(e.target.value))}
        >
          {offered.map(([seconds, text]) => (
            <option key={seconds} value={seconds || DEFAULT_TIMEOUT}>
              {text}
            </option>
          ))}
        </select>
        <span className="text-faint">If still running</span>
        <span className="seg">
          {(
            [
              ["skip", "skip"],
              ["allow", "run anyway"],
            ] as const
          ).map(([mode, text]) => (
            <button
              key={mode}
              type="button"
              aria-pressed={overlap === mode}
              disabled={readOnly}
              onClick={() => setOverlap(mode)}
            >
              {text}
            </button>
          ))}
        </span>
      </span>
      <span />
      <span className="flex flex-wrap items-center gap-2.5">
        {fromFile ? (
          <span className="text-mut">
            From dock.toml: change it there and deploy. It can be disabled and
            run now here.
          </span>
        ) : !canDeploy ? (
          <span className="text-mut">Changing jobs needs deploy access.</span>
        ) : (
          <>
            <Act
              primary
              type="submit"
              cli={cli}
              disabled={!valid || !changed || set.isPending}
              reason={
                !name || nameError
                  ? "name the job first"
                  : !trimmed
                    ? "type a command first"
                    : !preview || "error" in preview
                      ? "fix the schedule first"
                      : !changed
                        ? "nothing changed yet"
                        : undefined
              }
            >
              {job ? "Save" : "Add job"}
            </Act>
            {onCancel && (
              <Act type="button" onClick={onCancel}>
                Cancel
              </Act>
            )}
            <ErrorText error={set.error} />
          </>
        )}
      </span>
    </form>
  );
}

/** What started a run: the schedule, or who ran it now. */
function Trigger({ run }: { run: CommandRun }) {
  if (run.trigger === "schedule")
    return <span className="text-mut">schedule</span>;
  return (
    <span>
      run now by{" "}
      <Actor name={run.created_by} kind={actorKind(run.created_by)} />
    </span>
  );
}

/** A job's latest runs, newest first; a row opens its output under it. */
function History({
  serviceRef: ref,
  job,
  open,
  onOpen,
  canRead,
}: {
  serviceRef: ServiceRef;
  job: string;
  open: number | null;
  onOpen: (id: number | null) => void;
  canRead: boolean;
}) {
  const runs = useQuery(jobQueries.runs(ref, job));
  if (runs.error) return <ErrorText error={runs.error} />;
  if (!runs.data) return <p className="text-mut">Loading history.</p>;
  if (runs.data.length === 0) return <p className="text-mut">No runs yet.</p>;
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Status</th>
            <th>Exit</th>
            <th>Trigger</th>
            <th>Started</th>
            <th>Took</th>
            <th>Release</th>
          </tr>
        </thead>
        <tbody>
          {runs.data.map((r) => {
            const opened = open === r.id;
            return (
              <Fragment key={r.id}>
                <tr className={opened ? "bg-sel" : undefined}>
                  <td>
                    <button
                      type="button"
                      aria-expanded={opened}
                      className="hover:text-fg"
                      onClick={() => onOpen(opened ? null : r.id)}
                    >
                      <span aria-hidden className="text-faint">
                        {opened ? "▾" : "▸"}
                      </span>{" "}
                      <StateText state={runStates[r.status]} />
                    </button>
                  </td>
                  <td className="font-mono">{r.exit_code ?? ""}</td>
                  <td className="w-full">
                    <Trigger run={r} />
                  </td>
                  <td className="font-mono" title={ago(r.started_at)}>
                    {day(r.started_at)} {clock(r.started_at)}
                  </td>
                  <td className="font-mono">
                    {r.status === "skipped" ? "" : took(r)}
                  </td>
                  <td className="font-mono">
                    {r.release ? `r${r.release}` : ""}
                  </td>
                </tr>
                {opened && (
                  <tr>
                    <td colSpan={6} className="pb-3 whitespace-normal">
                      <RunOutput serviceRef={ref} run={r} canRead={canRead} />
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

/**
 * An opened run: its error and its output's tail, read once it has ended
 * (finished runs never change). Output takes deploy; skipped runs have
 * none, only why.
 */
function RunOutput({
  serviceRef: ref,
  run: r,
  canRead,
}: {
  serviceRef: ServiceRef;
  run: CommandRun;
  canRead: boolean;
}) {
  const ended = !isRunning(r);
  const skipped = r.status === "skipped";
  const full = useQuery({
    ...runQueries.one(ref, r.id),
    enabled: ended && canRead && !skipped,
  });
  return (
    <div className="flex flex-col gap-2">
      {r.error && (
        <p className={skipped ? "text-mut" : "text-fail"}>{r.error}</p>
      )}
      {skipped ? null : !ended ? (
        <p className="text-mut">Output appears when it ends.</p>
      ) : !canRead ? (
        <p className="text-mut">Reading output needs deploy access.</p>
      ) : full.error ? (
        <ErrorText error={full.error} />
      ) : !full.data ? (
        <p className="text-mut">Loading output.</p>
      ) : full.data.output ? (
        <Output text={full.data.output} />
      ) : (
        <p className="text-mut">No output.</p>
      )}
    </div>
  );
}
