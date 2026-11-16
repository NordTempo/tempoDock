import type { ReactNode } from "react";
import type { Release } from "../../data/types";
import { useNow } from "../../ui/clock";
import { elapsed } from "../../ui/format";
import { Glyph } from "../../ui/kit";
import { inFlight } from "../../ui/status";
import { errorSummary } from "./releases";

type StepState = "done" | "running" | "failed" | "pending" | "skipped";

type Step = {
  name: string;
  state: StepState;
  ms?: number;
  detail: ReactNode;
};

const stepOrder = [
  "queued",
  "building",
  "starting",
  "health_checking",
  "active",
];

const at = (r: Release, pattern: RegExp, last = false) => {
  const event = last
    ? r.events?.findLast((e) => pattern.test(e.message))
    : r.events?.find((e) => pattern.test(e.message));
  return (
    event && { time: Date.parse(event.created_at), message: event.message }
  );
};

// What the build step does: building or pulling, or waiting for a build
// slot or the platform registry, which write no build output.
const buildPhase = /^(Building|Pulling|Waiting for)/;

/**
 * A release's deploy steps (build, start, health check, cutover) with
 * timings from its progress events. `serving` is the release that keeps
 * traffic until cutover. `now` ends the running step's time (see
 * `ReleaseSteps`); finished steps use the server's timestamps only.
 */
export function deploySteps(
  r: Release,
  serving: number | null,
  stateful: boolean,
  now: number,
): Step[] {
  const created = Date.parse(r.created_at);
  const end = r.finished_at ? Date.parse(r.finished_at) : now;
  const build = at(r, buildPhase);
  // The newest of them says what a running build step waits on now.
  const phase = at(r, buildPhase, true);
  const built = at(r, /^Built /);
  const started = at(r, /^Started container /);
  const healthy = at(r, /^Health check passed/);
  const switched = at(r, / is live$/);
  const failed = r.status === "failed";

  // Which step the release is on, or failed on: the stored failure
  // (code, else the error's own words) decides over progress events, so
  // this page and the deploy window name the same cause.
  let current = Math.max(0, stepOrder.indexOf(r.status) - 1);
  if (failed) {
    const error = r.error ?? "";
    current =
      r.error_code === "build_failed" || /^(build|pull)/i.test(error)
        ? 0
        : r.error_code === "health_check_failed" || /^health check/i.test(error)
          ? 2
          : !started
            ? 0
            : !healthy
              ? 2
              : 3;
  }
  if (r.status === "active" || r.status === "superseded") current = 4;
  const state = (i: number): StepState =>
    i < current
      ? "done"
      : i > current
        ? failed
          ? "skipped"
          : "pending"
        : failed
          ? "failed"
          : "running";

  const span = (from?: number, to?: number) =>
    from !== undefined && to !== undefined ? to - from : undefined;
  const buildStart = build?.time ?? created;
  const buildEnd = built?.time ?? started?.time;
  const container = started?.message.replace(/^Started container /, "");
  const check = r.config.health_check_path
    ? `GET ${r.config.health_check_path}`
    : `TCP connect to :${r.config.port}`;
  const old = serving && serving !== r.number ? `r${serving}` : null;

  return [
    {
      name: "Build",
      state: state(0),
      ms: span(buildStart, buildEnd ?? (current === 0 ? end : undefined)),
      detail:
        state(0) === "failed" ? (
          <span className="text-fail">{errorSummary(r)}</span>
        ) : (
          <span className="text-mut">
            {(state(0) === "running" ? phase : build)?.message ??
              (r.image_ref ? `image ${r.image_ref}` : "waiting")}
          </span>
        ),
    },
    {
      name: "Start",
      state: state(1),
      ms: span(buildEnd, started?.time),
      // The container's name (dock_p1_e1_s1_r8) only in its title.
      detail: container ? (
        <span className="text-mut" title={container}>
          Container started
          {old && (stateful ? ` after stopping ${old}` : ` next to ${old}`)}
        </span>
      ) : (
        <span className="text-mut">Start the container</span>
      ),
    },
    {
      name: "Health check",
      state: state(2),
      ms: span(
        started?.time,
        healthy?.time ?? (current === 2 ? end : undefined),
      ),
      detail:
        state(2) === "failed" ? (
          <span>
            <span className="font-mono">{check}</span>:{" "}
            <span className="text-fail">{errorSummary(r)}</span>
          </span>
        ) : state(2) === "done" ? (
          <span className="text-mut">
            <span className="font-mono">{check}</span> passed
          </span>
        ) : (
          <span>
            <span className="font-mono">{check}</span>
            {state(2) === "running" && (
              <span className="text-mut"> until it answers</span>
            )}
          </span>
        ),
    },
    {
      name: "Traffic moved",
      state: state(3),
      ms: span(healthy?.time, switched?.time),
      detail:
        state(3) === "failed" ? (
          <span className="text-fail">{errorSummary(r)}</span>
        ) : (
          <span className="text-mut">
            {state(3) === "skipped"
              ? old
                ? `Skipped. Traffic stayed on ${old}.`
                : "Skipped."
              : state(3) === "done"
                ? "Traffic switched"
                : `Switch traffic to r${r.number}${old && !stateful ? `, then stop ${old}` : ""}`}
          </span>
        ),
    },
  ];
}

const glyphs: Record<StepState, ReactNode> = {
  done: <span className="text-ok">✓</span>,
  running: (
    <span className="text-prog">
      <Glyph char="◐" />
    </span>
  ),
  failed: <span className="text-fail">✕</span>,
  pending: <span className="text-mut">○</span>,
  skipped: <span className="text-mut">–</span>,
};

const rowTint: Partial<Record<StepState, string>> = {
  running: "bg-prog-bg",
  failed: "bg-fail-bg",
};

/** The step table: glyph, name, duration, what happened. */
export function StepTable({ steps }: { steps: Step[] }) {
  return (
    <div className="overflow-x-auto">
      <div className="min-w-[560px]">
        {steps.map((step) => (
          <div key={step.name} className={`step ${rowTint[step.state] ?? ""}`}>
            {glyphs[step.state]}
            <span
              className={
                step.state === "pending" || step.state === "skipped"
                  ? "text-mut"
                  : ""
              }
            >
              {step.name}
            </span>
            <span className="text-right font-mono">
              {step.ms !== undefined && step.state !== "pending"
                ? elapsed(step.ms)
                : ""}
            </span>
            <span className="min-w-0">{step.detail}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * A release's step table whose running step counts live: only this
 * component re-renders on the clock's tick, not the page around it.
 */
export function ReleaseSteps({
  release,
  serving,
  stateful,
}: {
  release: Release;
  serving: number | null;
  stateful: boolean;
}) {
  const now = useNow(inFlight(release.status));
  return <StepTable steps={deploySteps(release, serving, stateful, now)} />;
}
