import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Fragment, useLayoutEffect, useRef, useState } from "react";
import {
  useCanDeploy,
  useDeployBlock,
  useService,
  useVolumes,
} from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { ago, clock, day, elapsed } from "../../ui/format";
import {
  Act,
  Actor,
  actorKind,
  Check,
  ErrorText,
  Panel,
  StateText,
} from "../../ui/kit";
import type { State } from "../../ui/status";
import {
  type CommandRun,
  isRunning,
  type RunVolumes,
  runQueries,
  useRunCommand,
} from "./data";

/**
 * A service's Run tab (ADR 0048): a command box over the latest runs, the
 * selected one opened under its row with its output. Runs go in the
 * background; run events keep the list current.
 */
export function RunPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const runs = useSuspenseQuery(runQueries.list(ref)).data;
  const canDeploy = useCanDeploy(ref);
  const [selected, setSelected] = useState<number | null>(null);
  return (
    <>
      {service.active_release ? (
        <RunForm serviceRef={ref} onStarted={setSelected} />
      ) : (
        <p className="text-mut">
          Deploy first: a command runs in the active release's image.
        </p>
      )}
      <History
        serviceRef={ref}
        runs={runs}
        selected={selected}
        onSelect={setSelected}
        canRead={canDeploy}
      />
    </>
  );
}

/** Timeouts offered; 0 leaves the API's default. The Jobs tab's too. */
export const timeouts = [
  [0, "1h"],
  [60, "1m"],
  [300, "5m"],
  [900, "15m"],
  [1800, "30m"],
  [7200, "2h"],
  [21_600, "6h"],
  [86_400, "24h"],
] as const;

const volumeModes: [RunVolumes, string][] = [
  ["read", "read-only"],
  ["none", "none"],
  ["write", "write"],
];

/** A word outside this set makes the CLI hint quote the command. */
const shellSafe = /^[\w@%+=:,./-]+$/;

/** The command as one shell word, as `dock run --` sends it. */
export const quote = (command: string) =>
  shellSafe.test(command) ? command : `'${command.replaceAll("'", `'\\''`)}'`;

/**
 * The command box: Enter runs it. Stateful services that aren't database
 * templates pick how their volumes mount; read-only unless asked. Write
 * takes a snapshot of them first unless that is turned off.
 */
function RunForm({
  serviceRef: ref,
  onStarted,
}: {
  serviceRef: ServiceRef;
  onStarted: (id: number) => void;
}) {
  const { service } = useService(ref);
  // A database's volume is never mounted; it is reached over the network.
  const applied = useVolumes(ref).some((v) => !v.pending);
  const mounts = applied && !service.template;
  const canDeploy = useCanDeploy(ref);
  const blocked = useDeployBlock(ref);
  const run = useRunCommand();
  const [command, setCommand] = useState("");
  const [timeout, setTimeoutSeconds] = useState(0);
  const [volumes, setVolumes] = useState<RunVolumes>("read");
  const [snapshot, setSnapshot] = useState(true);
  const writes = mounts && volumes === "write";

  const reason = !canDeploy ? "Needs deploy access" : blocked;
  const trimmed = command.trim();
  const cli = [
    `dock run -s ${ref.service} -e ${ref.environment} --detach`,
    mounts && volumes !== "read" && `--volumes ${volumes}`,
    writes && !snapshot && "--no-snapshot",
    timeout > 0 && `--timeout ${timeouts.find(([s]) => s === timeout)?.[1]}`,
    `-- ${trimmed ? quote(trimmed) : "<command>"}`,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <form
      aria-label="Run a command"
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (!trimmed || reason) return;
        run.mutate(
          {
            ref,
            req: {
              command: trimmed,
              ...(timeout > 0 && { timeout_seconds: timeout }),
              ...(mounts && { volumes }),
              ...(writes && !snapshot && { no_snapshot: true }),
            },
          },
          { onSuccess: (r) => onStarted(r.id) },
        );
      }}
    >
      <div className="flex items-center gap-2.5">
        <input
          aria-label="Command"
          placeholder="a command, e.g. npm run migrate"
          spellCheck={false}
          autoComplete="off"
          maxLength={16_384}
          disabled={!!reason}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          className="field min-w-0 flex-1 font-mono"
        />
        <Act
          primary
          type="submit"
          cli={cli}
          disabled={!!reason || !trimmed || run.isPending}
          reason={reason}
        >
          Run
        </Act>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        {mounts && (
          <span className="flex items-center gap-1.5">
            <span className="text-faint">Volumes</span>
            <span className="seg">
              {volumeModes.map(([mode, label]) => (
                <button
                  key={mode}
                  type="button"
                  aria-pressed={volumes === mode}
                  disabled={!!reason}
                  onClick={() => setVolumes(mode)}
                >
                  {label}
                </button>
              ))}
            </span>
          </span>
        )}
        <label className="flex items-center gap-1.5">
          <span className="text-faint">Timeout</span>
          <select
            className="field py-0.5 font-mono text-xs"
            value={timeout}
            disabled={!!reason}
            onChange={(e) => setTimeoutSeconds(Number(e.target.value))}
          >
            {timeouts.map(([seconds, label]) => (
              <option key={seconds} value={seconds}>
                {label}
              </option>
            ))}
          </select>
        </label>
        {writes && (
          <>
            <Check
              label="snapshot first"
              title="Snapshot the volumes before the run; a failed snapshot refuses it"
              checked={snapshot}
              disabled={!!reason}
              onChange={setSnapshot}
            />
            <span className="text-warn">
              Mounts read-write next to the running {ref.service}: only for
              storage that takes a second writer.
            </span>
          </>
        )}
        {reason && <span className="text-mut">{reason}.</span>}
        <ErrorText error={run.error} />
      </div>
    </form>
  );
}

/** A run's status as the Run and Jobs tabs show it. */
export const runStates: Record<CommandRun["status"], State> = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  succeeded: { tone: "ok", glyph: "●", label: "succeeded" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
  timed_out: { tone: "fail", glyph: "✕", label: "timed out" },
  canceled: { tone: "mut", glyph: "○", label: "canceled" },
  interrupted: { tone: "mut", glyph: "○", label: "interrupted" },
  skipped: { tone: "mut", glyph: "–", label: "skipped" },
};

/** How long a run took, once it ended. */
export const took = (r: CommandRun) =>
  r.finished_at
    ? elapsed(Date.parse(r.finished_at) - Date.parse(r.started_at))
    : "";

/** The latest runs, newest first; a row's command opens it. */
function History({
  serviceRef: ref,
  runs,
  selected,
  onSelect,
  canRead,
}: {
  serviceRef: ServiceRef;
  runs: CommandRun[];
  selected: number | null;
  onSelect: (id: number | null) => void;
  canRead: boolean;
}) {
  if (runs.length === 0) return <p className="py-2 text-mut">No runs yet.</p>;
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Status</th>
            <th>Exit</th>
            <th>Command</th>
            <th>Who</th>
            <th>Started</th>
            <th>Took</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => {
            const open = selected === r.id;
            return (
              <Fragment key={r.id}>
                <tr className={open ? "bg-sel" : undefined}>
                  <td>
                    <StateText state={runStates[r.status]} />
                  </td>
                  <td className="font-mono">{r.exit_code ?? ""}</td>
                  <td className="w-full max-w-0">
                    <button
                      type="button"
                      aria-expanded={open}
                      title={r.command}
                      className="block w-full truncate text-left font-mono hover:text-fg"
                      onClick={() => onSelect(open ? null : r.id)}
                    >
                      <span aria-hidden className="text-faint">
                        {open ? "▾" : "▸"}
                      </span>{" "}
                      {r.command}
                    </button>
                  </td>
                  <td>
                    <Actor name={r.created_by} kind={actorKind(r.created_by)} />
                  </td>
                  <td className="font-mono" title={ago(r.started_at)}>
                    {day(r.started_at)} {clock(r.started_at)}
                  </td>
                  <td className="font-mono">{took(r)}</td>
                </tr>
                {open && (
                  <tr>
                    <td colSpan={6} className="pb-3 whitespace-normal">
                      <RunDetail serviceRef={ref} run={r} canRead={canRead} />
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
 * An opened run: where it ran, its error, and its output's tail, read
 * once it has ended (finished runs never change). Output takes deploy.
 */
function RunDetail({
  serviceRef: ref,
  run: r,
  canRead,
}: {
  serviceRef: ServiceRef;
  run: CommandRun;
  canRead: boolean;
}) {
  const ended = !isRunning(r);
  const full = useQuery({
    ...runQueries.one(ref, r.id),
    enabled: ended && canRead,
  });
  return (
    <div className="flex flex-col gap-2">
      <p className="text-mut">
        <span className="font-mono">r{r.release}</span> · volumes {r.volumes} ·
        timeout {elapsed(r.timeout_seconds * 1000)}
        {r.tty && " · terminal"}
        {r.snapshot ? ` · snapshot ${r.snapshot} taken before` : ""}
      </p>
      {r.command.includes("\n") && (
        <pre className="max-h-40 overflow-auto font-mono text-xs whitespace-pre-wrap text-sec">
          {r.command}
        </pre>
      )}
      {r.error && <p className="text-fail">{r.error}</p>}
      {!ended ? (
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
        <p className="text-mut">
          {r.tty ? "Terminal sessions keep no output." : "No output."}
        </p>
      )}
    </div>
  );
}

/** A run's output tail, scrolled to its end: the last lines matter most. */
export function Output({ text }: { text: string }) {
  const pre = useRef<HTMLPreElement>(null);
  useLayoutEffect(() => {
    if (pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, []);
  return (
    <pre
      ref={pre}
      className="max-h-96 overflow-auto rounded-ctl border border-line bg-side px-3 py-2.5 font-mono text-xs whitespace-pre-wrap wrap-anywhere text-sec"
    >
      {text}
    </pre>
  );
}
