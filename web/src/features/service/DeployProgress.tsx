import { useBuildLog } from "../../data/live";
import { useRelease } from "../../data/queries";
import type { Release, ServiceRef } from "../../data/types";
import { clockPrecise } from "../../ui/format";
import { Glyph, Heading } from "../../ui/kit";
import { changeText, type Tone } from "../../ui/status";

/** Deploy steps in order, each with the release status that runs it. */
const steps = [
  ["build", "building"],
  ["start", "starting"],
  ["health check", "health_checking"],
  ["cutover", "active"],
] as const;

const order: Release["status"][] = [
  "queued",
  "building",
  "starting",
  "health_checking",
  "active",
];

type StepState = "done" | "running" | "pending" | "failed";

const stepTone = {
  done: "ok",
  running: "run",
  failed: "bad",
  pending: "off",
} as const satisfies Record<StepState, Tone>;

function stepState(status: Release["status"], step: number): StepState {
  const at = order.indexOf(status);
  if (status === "active") return "done";
  if (at < 0) return status === "failed" ? "failed" : "pending";
  const running = at - 1;
  return step < running ? "done" : step === running ? "running" : "pending";
}

/**
 * Build, start, health check, cutover for the release in flight, with its
 * progress lines and the build output tail. All of it arrives live.
 */
export function DeployProgress({
  serviceRef: ref,
  number,
  serving,
}: {
  serviceRef: ServiceRef;
  number: number;
  /** Release that keeps serving traffic until cutover. */
  serving: number | null;
}) {
  const release = useRelease(ref, number);
  const build = useBuildLog(ref, number);
  const events = release.events ?? [];

  return (
    <section>
      <Heading
        note={`${release.created_by} · ${changeText(release)} · started ${clockPrecise(release.created_at).slice(0, 8)}`}
      >
        r{release.number}{" "}
        {release.status === "failed" ? "failed" : "in progress"}
      </Heading>
      <div className="flex gap-5 pb-2">
        {steps.map(([label], i) => {
          const state = stepState(release.status, i);
          return (
            <span
              key={label}
              className={`flex items-baseline ${state === "pending" ? "text-dim" : ""}`}
            >
              <Glyph tone={stepTone[state]} hollow={state === "pending"} />
              {label}
            </span>
          );
        })}
      </div>
      <div className="font-mono text-xs leading-[1.55]">
        {events.map((e, i) => (
          <div
            key={`${e.created_at} ${e.message}`}
            className={i === events.length - 1 ? "" : "text-dim"}
          >
            <span className="text-faint">{clockPrecise(e.created_at)}</span>{" "}
            {e.message}
          </div>
        ))}
        {release.error && <div className="text-bad">{release.error}</div>}
      </div>
      {build.lines.length > 0 && (
        <div className="mt-1.5 border-l border-line2 pl-2.5 font-mono text-xs leading-[1.55] whitespace-pre text-dim">
          {build.lines
            .slice(0, 8)
            .reverse()
            .map((line) => (
              <div key={line.key} className="truncate">
                {line.text}
              </div>
            ))}
        </div>
      )}
      {serving !== null && (
        <p className="pt-2 text-xs text-dim">
          r{serving} keeps serving until cutover. If the check fails, r
          {release.number} is stopped.
        </p>
      )}
    </section>
  );
}
