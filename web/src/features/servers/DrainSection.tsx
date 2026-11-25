import { useState } from "react";
import { useServers } from "../../data/queries";
import type { EnvironmentKind, Server, ServerDetail } from "../../data/types";
import { ago, elapsed, plural, size } from "../../ui/format";
import { Act, Confirm, ErrorText, Panel } from "../../ui/kit";
import { kindLabel } from "../../ui/status";
import { heldFor } from "../move/data";
import {
  type DrainItem,
  type DrainRequest,
  drainOpen,
  type ServerDrain,
  useCancelDrain,
  useDrain,
  usePlanDrain,
  useResumeDrain,
  useStartDrain,
} from "./drain";

// The server page's Drain section (ADR 0063): plan moving every
// environment (or every one of a kind) off the server, start it, follow it
// one environment at a time, and see what the server forwards after. The
// drain's state comes from server_drain events (drain.ts).

type Kind = EnvironmentKind | "";

const kindChoices: [Kind, string][] = [
  ["", "Every environment"],
  ["non_production", "Non-production only"],
  ["production", "Production only"],
];

const holds = [30, 60, 120];

/**
 * The server page's Drain row, opened: the running drain, or the planner
 * and the last result.
 */
export function DrainSection({ detail }: { detail: ServerDetail }) {
  const name = detail.server.name;
  const drain = useDrain(name);
  return (
    <div className="flex flex-col gap-2.5">
      {drainOpen(drain) && drain ? (
        <DrainRun drain={drain} />
      ) : (
        <>
          {drain?.id ? <DrainResult drain={drain} /> : null}
          <Planner detail={detail} />
        </>
      )}
      {drain && (drain.forwards ?? []).length > 0 && <Forwards drain={drain} />}
    </div>
  );
}

/**
 * Kinds the server no longer accepts but still holds environments of,
 * with how many: what unticking a kind left behind.
 */
function leftBehind(detail: ServerDetail): [EnvironmentKind, number][] {
  const s = detail.server;
  const out: [EnvironmentKind, number][] = [];
  for (const kind of ["non_production", "production"] as const) {
    const accepts =
      kind === "production" ? s.accepts_production : s.accepts_non_production;
    const n = detail.placements
      .filter((p) => p.kind === kind)
      .reduce((sum, p) => sum + (p.environments?.length ?? 0), 0);
    if (!accepts && n > 0) out.push([kind, n]);
  }
  return out;
}

/** The CLI equivalent of a drain request. */
function drainCli(name: string, req: DrainRequest) {
  const parts = [`dock server drain ${name}`];
  if (req.kind) parts.push(`--kind ${kindLabel[req.kind]}`);
  for (const t of req.targets ?? []) parts.push(`--to ${t.project}=${t.to}`);
  if (req.max_hold && req.max_hold !== 30)
    parts.push(`--max-hold ${req.max_hold}`);
  return parts.join(" ");
}

/** Why a server can't take an environment of kind, or "" when it can. */
function unfit(s: Server, kind: EnvironmentKind) {
  if (s.status !== "online") return s.status;
  if (s.drain) return "draining";
  const accepts =
    kind === "production" ? s.accepts_production : s.accepts_non_production;
  return accepts ? "" : `no ${kindLabel[kind]}`;
}

/** What to drain, the plan with a target per project, and Start. */
function Planner({ detail }: { detail: ServerDetail }) {
  const name = detail.server.name;
  const servers = useServers().filter(
    (s) => s.name !== name && s.status !== "revoked" && s.status !== "joining",
  );
  const plan = usePlanDrain();
  const start = useStartDrain();
  const [kind, setKind] = useState<Kind>("");
  const [hold, setHold] = useState(30);
  // A project's target, by "org/project", where the operator chose one.
  const [targets, setTargets] = useState<Record<string, string>>({});
  const [confirming, setConfirming] = useState(false);
  const req = (k: Kind, t: Record<string, string>): DrainRequest => ({
    kind: k || undefined,
    max_hold: hold,
    targets: Object.entries(t).map(([project, to]) => ({ project, to })),
  });
  const replan = (k: Kind, t: Record<string, string>) => {
    setKind(k);
    setTargets(t);
    plan.mutate({ server: name, req: req(k, t) });
  };
  const result = plan.data;
  const items = result?.items ?? [];
  const problems = result?.problems ?? [];
  const current = req(kind, targets);
  const dataBytes = items.reduce((n, i) => n + (i.data_bytes ?? 0), 0);
  const left = leftBehind(detail);
  return (
    <>
      {left.map(([k, n]) => (
        <div
          key={k}
          className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-warn-edge bg-warn-bg px-3 py-2 text-xs"
        >
          <span className="flex-1 text-sec">
            {plural(n, `${kindLabel[k]} environment`)} {n === 1 ? "is" : "are"}{" "}
            still on <span className="font-mono">{name}</span>, which no longer
            takes them.
          </span>
          <Act
            small
            primary
            cli={drainCli(name, { kind: k })}
            disabled={plan.isPending}
            onClick={() => replan(k, {})}
          >
            Plan moving them
          </Act>
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
        <select
          aria-label="What to drain"
          className="field py-[3px]"
          value={kind}
          onChange={(e) => {
            setKind(e.target.value as Kind);
            plan.reset();
          }}
        >
          {kindChoices.map(([k, label]) => (
            <option key={k} value={k}>
              {label}
            </option>
          ))}
        </select>
        <span className="text-mut">hold requests at most</span>
        <select
          aria-label="Longest hold per move"
          className="field py-[3px]"
          value={hold}
          onChange={(e) => {
            setHold(Number(e.target.value));
            plan.reset();
          }}
        >
          {holds.map((h) => (
            <option key={h} value={h}>
              {h} s
            </option>
          ))}
        </select>
        <Act
          small
          cli={`${drainCli(name, current)} --dry-run`}
          disabled={plan.isPending}
          onClick={() => replan(kind, targets)}
        >
          {result ? "Plan again" : "Plan"}
        </Act>
      </div>
      <ErrorText error={plan.error} />
      {result && items.length === 0 && (
        <p className="text-xs text-mut">
          Nothing to move: no {kind ? `${kindLabel[kind]} ` : ""}environment is
          on {name}.
        </p>
      )}
      {items.length > 0 && (
        <>
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Environment</th>
                  <th>Kind</th>
                  <th>To</th>
                  <th className="text-right">Data</th>
                  <th>Check</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <PlanRow
                    key={`${it.org}/${it.project}/${it.environment}`}
                    item={it}
                    servers={servers}
                    disabled={plan.isPending}
                    onTarget={(to) =>
                      replan(kind, {
                        ...targets,
                        [`${it.org}/${it.project}`]: to,
                      })
                    }
                  />
                ))}
              </tbody>
            </table>
          </Panel>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
            <Act
              primary
              cli={drainCli(name, current)}
              disabled={
                problems.length > 0 || plan.isPending || start.isPending
              }
              reason={
                problems.length > 0
                  ? `${plural(problems.length, "problem")} keep${problems.length === 1 ? "s" : ""} the drain from starting`
                  : undefined
              }
              onClick={() => setConfirming(true)}
            >
              Start drain
            </Act>
            <span className="text-mut">
              {plural(items.length, "environment")}, one at a time
              {dataBytes > 0 && `, ${size(dataBytes)} of data`}
            </span>
          </div>
        </>
      )}
      {confirming && (
        <Confirm
          action="Start drain"
          title={`Drain ${name}?`}
          cli={drainCli(name, current)}
          pending={start.isPending}
          error={start.error}
          onConfirm={() =>
            start.mutate(
              { server: name, req: current },
              {
                onSuccess: () => {
                  setConfirming(false);
                  plan.reset();
                },
              },
            )
          }
          onCancel={() => {
            setConfirming(false);
            start.reset();
          }}
        >
          Move {plural(items.length, "environment")} off{" "}
          <b className="font-mono font-medium">{name}</b>, one at a time? Each
          copies while it serves, then holds its requests for at most {hold}{" "}
          seconds while it switches; nothing is lost.{" "}
          <span className="text-mut">
            {name} takes no new {kind ? `${kindLabel[kind]} ` : ""}environments
            from now on.
          </span>
        </Confirm>
      )}
    </>
  );
}

/** One planned environment, with a choice of target for its project. */
function PlanRow({
  item: it,
  servers,
  disabled,
  onTarget,
}: {
  item: DrainItem;
  servers: Server[];
  disabled: boolean;
  onTarget: (to: string) => void;
}) {
  return (
    <tr>
      <td className="font-mono">
        {it.org}/{it.project}/{it.environment}
      </td>
      <td>{kindLabel[it.kind]}</td>
      <td>
        <select
          aria-label={`Server for ${it.project}/${it.environment}`}
          className="field py-[2px]"
          value={it.to}
          disabled={disabled}
          onChange={(e) => onTarget(e.target.value)}
        >
          {!it.to && <option value="">none</option>}
          {servers.map((s) => {
            const why = unfit(s, it.kind);
            return (
              <option key={s.id} value={s.name} disabled={!!why}>
                {s.name}
                {why && ` (${why})`}
              </option>
            );
          })}
        </select>
      </td>
      <td className="text-right font-mono text-mut">
        {it.stateful && it.data_bytes ? size(it.data_bytes) : "·"}
      </td>
      <td className="w-full whitespace-normal">
        {it.problem ? (
          <span className="text-fail">{it.problem}</span>
        ) : (
          <span className="text-ok">ready</span>
        )}
      </td>
    </tr>
  );
}

const itemTone: Record<DrainItem["status"], string> = {
  waiting: "text-faint",
  moving: "text-prog",
  moved: "text-ok",
  failed: "text-fail",
  skipped: "text-mut",
};

/** What a moving environment is doing: its step, or its latest pass. */
function stepWords(it: DrainItem) {
  const m = it.move;
  if (!m || it.status !== "moving") return it.status === "moved" ? "done" : "";
  if (m.step === "sync") {
    const s = (m.services ?? []).find(
      (s) => s.passes > 0 && s.status === "syncing",
    );
    if (s)
      return `sync, ${s.service} pass ${s.passes} took ${elapsed(s.last_pass_ms)}`;
  }
  if (m.status === "rolling_back") return "rolling back";
  return m.step;
}

/** A running or paused drain: what it does now, and the way on. */
function DrainRun({ drain }: { drain: ServerDrain }) {
  const name = drain.server;
  const resume = useResumeDrain();
  const cancel = useCancelDrain();
  const done = (drain.items ?? []).filter(
    (i) => i.status === "moved" || i.status === "skipped",
  ).length;
  const paused = drain.status === "paused";
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
        <span>
          <span className={paused ? "text-warn" : "text-prog"}>●</span>{" "}
          {paused ? "Paused" : "Draining"}, {done} of{" "}
          {(drain.items ?? []).length} moved
        </span>
        <span className="text-faint">
          started {drain.started_at && ago(drain.started_at)} by{" "}
          {drain.created_by}
        </span>
        <span className="ml-auto flex flex-wrap gap-2">
          {paused && (
            <>
              <Act
                small
                cli={`dock server drain ${name} --resume --skip`}
                disabled={resume.isPending}
                onClick={() => resume.mutate({ server: name, skip: true })}
              >
                Skip it
              </Act>
              <Act
                small
                primary
                cli={`dock server drain ${name} --resume`}
                disabled={resume.isPending}
                onClick={() => resume.mutate({ server: name, skip: false })}
              >
                Try again
              </Act>
            </>
          )}
          <Act
            small
            cli={`dock server drain ${name} --cancel`}
            disabled={cancel.isPending}
            onClick={() => cancel.mutate({ server: name })}
          >
            {paused ? "Cancel the drain" : "Cancel after this move"}
          </Act>
        </span>
      </div>
      {paused && drain.error && (
        <p className="text-xs text-fail">{drain.error}</p>
      )}
      <ErrorText error={resume.error ?? cancel.error} />
      <Items drain={drain} />
    </>
  );
}

/** A drain's environments with how far each got. */
function Items({ drain }: { drain: ServerDrain }) {
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Environment</th>
            <th>To</th>
            <th>Status</th>
            <th>Step</th>
            <th className="text-right">Copied</th>
            <th className="text-right">Held</th>
          </tr>
        </thead>
        <tbody>
          {(drain.items ?? []).map((it) => {
            const copied =
              (it.move?.services ?? []).reduce(
                (n, s) => n + s.copied_bytes,
                0,
              ) ?? 0;
            return (
              <tr key={`${it.org}/${it.project}/${it.environment}`}>
                <td className="font-mono">
                  {it.org}/{it.project}/{it.environment}
                </td>
                <td className="font-mono">{it.to}</td>
                <td className={itemTone[it.status]}>{it.status}</td>
                <td className="w-full whitespace-normal text-mut">
                  {it.status === "failed" || it.status === "skipped" ? (
                    <span className="text-fail">{it.error}</span>
                  ) : (
                    stepWords(it)
                  )}
                </td>
                <td className="text-right font-mono text-mut">
                  {copied ? size(copied) : "·"}
                </td>
                <td className="text-right font-mono text-mut">
                  {(it.move && heldFor(it.move)) || "·"}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Panel>
  );
}

/** The last drain's outcome, once it ended. */
function DrainResult({ drain }: { drain: ServerDrain }) {
  const moved = (drain.items ?? []).filter((i) => i.status === "moved").length;
  const skipped = (drain.items ?? []).filter(
    (i) => i.status === "skipped",
  ).length;
  const when = drain.finished_at ? ago(drain.finished_at) : "";
  return (
    <p className="text-xs">
      {drain.status === "succeeded" ? (
        <>
          <span className="text-ok">●</span> Drained {when}:{" "}
          {plural(moved, "environment")} moved
          {skipped > 0 && `, ${skipped} skipped`}.
        </>
      ) : (
        <>
          <span className="text-mut">○</span> The last drain was cancelled{" "}
          {when}, after {plural(moved, "environment")} moved.
        </>
      )}
    </p>
  );
}

/** Ports the server forwards for services that moved away from it. */
function Forwards({ drain }: { drain: ServerDrain }) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="text-xs text-mut">
        Forwards: requests that still reach {drain.server} (DNS caches, records
        Dock doesn't manage) go to the server the service is on now, until{" "}
        {drain.server} is removed.
      </p>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Service</th>
              <th>Now on</th>
              <th>Port</th>
              <th>Since</th>
            </tr>
          </thead>
          <tbody>
            {(drain.forwards ?? []).map((f) => (
              <tr key={f.port}>
                <td className="font-mono">
                  {f.project}/{f.environment}/{f.service}
                </td>
                <td className="font-mono">{f.now_on}</td>
                <td className="font-mono text-mut">{f.port}</td>
                <td className="w-full text-mut">{ago(f.since)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}
