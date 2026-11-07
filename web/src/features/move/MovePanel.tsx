import { useState } from "react";
import { useServers } from "../../data/queries";
import type { EnvironmentKind, EnvRef, Server } from "../../data/types";
import { clockSec, elapsed, size } from "../../ui/format";
import { Act, Confirm, Panel } from "../../ui/kit";
import { kindLabel } from "../../ui/status";
import {
  type EnvironmentMove,
  heldFor,
  type MoveStep,
  moveRunning,
  useMove,
  useMoveEnvironment,
} from "./data";

// An environment's server and its move (ADRs 0053 and 0063), opened from
// the environment's heading on the project overview: operators pick
// another server and move it there; everyone sees the running or latest
// move's steps, hold, services and what is left to do. Loaded when opened.

/**
 * The "Server" row (operators only) and the move's progress. `server` is
 * where the environment is now.
 */
export default function MovePanel({
  env,
  server,
  kind,
  operator,
}: {
  env: EnvRef;
  server: string;
  kind: EnvironmentKind;
  operator: boolean;
}) {
  const move = useMove(env);
  return (
    <div className="mb-2.5 flex flex-col gap-2.5">
      {operator && (
        <ServerRow
          env={env}
          server={server}
          kind={kind}
          running={moveRunning(move)}
        />
      )}
      {move ? (
        <MoveProgress move={move} />
      ) : (
        !operator && <p className="text-xs text-mut">Never moved.</p>
      )}
    </div>
  );
}

/** Why a server can't take the environment, or "" when it can. */
function unfit(s: Server, kind: EnvironmentKind) {
  if (s.status !== "online") return s.status;
  const accepts =
    kind === "production" ? s.accepts_production : s.accepts_non_production;
  return accepts ? "" : `no ${kindLabel[kind]}`;
}

/** The current server, a choice of the others and Move, behind a confirm. */
function ServerRow({
  env,
  server,
  kind,
  running,
}: {
  env: EnvRef;
  server: string;
  kind: EnvironmentKind;
  running: boolean;
}) {
  const servers = useServers().filter(
    (s) => s.name !== server && s.status !== "revoked",
  );
  const move = useMoveEnvironment();
  const [to, setTo] = useState("");
  const [confirming, setConfirming] = useState(false);
  const cli = `dock env move ${env.environment} --to ${to || "<server>"}`;
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
        <span className="w-14 text-mut">Server</span>
        <span className="font-mono">{server}</span>
        {servers.length === 0 ? (
          <span className="text-mut">No other server to move to.</span>
        ) : (
          <>
            <span className="text-faint" aria-hidden>
              →
            </span>
            <select
              aria-label={`Server to move ${env.environment} to`}
              value={to}
              disabled={running}
              onChange={(e) => {
                setTo(e.target.value);
                setConfirming(false);
              }}
              className="field py-[3px]"
            >
              <option value="">choose a server</option>
              {servers.map((s) => {
                const why = unfit(s, kind);
                return (
                  <option key={s.id} value={s.name} disabled={!!why}>
                    {s.name}
                    {why && ` (${why})`}
                  </option>
                );
              })}
            </select>
            <Act
              small
              primary
              cli={cli}
              disabled={!to || running || move.isPending}
              onClick={() => setConfirming(true)}
            >
              Move
            </Act>
          </>
        )}
      </div>
      {confirming && to && (
        <Confirm
          action={`Move to ${to}`}
          cli={cli}
          pending={move.isPending}
          error={move.error}
          onConfirm={() =>
            move.mutate(
              { env, to },
              {
                onSuccess: () => {
                  setConfirming(false);
                  setTo("");
                },
              },
            )
          }
          onCancel={() => {
            setConfirming(false);
            move.reset();
          }}
        >
          Move <b className="font-semibold">{env.environment}</b> from{" "}
          <span className="font-mono">{server}</span> to{" "}
          <span className="font-mono">{to}</span>? Volumes copy while it serves,
          then requests wait a few seconds while it switches. Domains not routed
          through Cloudflare need their DNS changed after; the old server
          forwards them meanwhile.
        </Confirm>
      )}
    </>
  );
}

const steps: Exclude<MoveStep, "done">[] = [
  "prepare",
  "sync",
  "stop",
  "copy",
  "start",
  "switch",
  "cleanup",
];

/** A step's colour: done, current (or where it failed), or still to come. */
function stepTone(move: EnvironmentMove, i: number) {
  const at = move.step === "done" ? steps.length : steps.indexOf(move.step);
  if (i < at || move.status === "succeeded") return "text-ok";
  if (i > at) return "text-faint";
  return move.status === "failed" ? "text-fail" : "text-prog";
}

/** How long requests were held, without a counter that repaints. */
function held(move: EnvironmentMove) {
  const from = move.downtime_from;
  if (!from) return moveRunning(move) ? "not held yet" : "never held";
  const to = move.downtime_to;
  if (!to) return `held since ${clockSec(from)}`;
  return `held ${heldFor(move)}`;
}

const serviceTone: Record<string, string> = {
  waiting: "text-faint",
  healthy: "text-ok",
  failed: "text-fail",
};

/** The move's steps, downtime, services, error and notes. */
function MoveProgress({ move }: { move: EnvironmentMove }) {
  const running = moveRunning(move);
  const services = move.services ?? [];
  const notes = move.notes ?? [];
  const verb =
    move.status === "succeeded"
      ? "Moved"
      : move.status === "failed"
        ? "Move failed"
        : move.status === "rolling_back"
          ? "Rolling back"
          : "Moving";
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span className="w-14 text-mut">
          {running ? "Moving" : "Last move"}
        </span>
        <span>
          {verb} <span className="font-mono">{move.from}</span> →{" "}
          <span className="font-mono">{move.to}</span>
        </span>
        <span title={`step: ${move.step}`}>
          {steps.map((s, i) => (
            <span key={s} className={stepTone(move, i)}>
              {i > 0 && <span className="text-faint"> · </span>}
              {s}
            </span>
          ))}
        </span>
        <span className="text-mut">{held(move)}</span>
        <span className="text-faint">
          by {move.created_by} at {clockSec(move.started_at)}
        </span>
      </div>
      {move.status === "failed" && move.error && (
        <p className="text-xs text-fail">rolled back: {move.error}</p>
      )}
      {move.status === "rolling_back" && move.error && (
        <p className="text-xs text-prog">rolling back: {move.error}</p>
      )}
      {services.length > 0 && (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th>Stateful</th>
                <th>Status</th>
                <th>Copied</th>
                <th>Passes</th>
                <th>Error</th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <tr key={s.service}>
                  <td className="font-mono">{s.service}</td>
                  <td className="text-mut">{s.stateful ? "yes" : "no"}</td>
                  <td
                    className={
                      serviceTone[s.status] ??
                      (running ? "text-prog" : "text-mut")
                    }
                  >
                    {s.status}
                  </td>
                  <td className="font-mono text-mut">
                    {s.stateful ? size(s.copied_bytes) : "·"}
                  </td>
                  <td className="font-mono text-mut">
                    {s.stateful && s.passes > 0
                      ? `${s.passes}, last ${elapsed(s.last_pass_ms)}`
                      : "·"}
                  </td>
                  <td className="w-full whitespace-normal text-fail">
                    {s.error}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      {notes.length > 0 && (
        <ul className="flex list-disc flex-col gap-0.5 pl-4 text-xs text-sec">
          {notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </>
  );
}
