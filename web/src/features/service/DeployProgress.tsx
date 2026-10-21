import type { Release } from "../../data/types";
import { clockPrecise, duration } from "../../ui/format";
import { Glyph, Heading } from "../../ui/kit";
import { changeText, stepLabel, type Tone } from "../../ui/status";

const stepTone = {
  done: "ok",
  running: "run",
  failed: "bad",
  pending: "off",
} as const satisfies Record<string, Tone>;

/** Build, start, health check, cutover for the release in flight. */
export function DeployProgress({
  release,
  serving,
}: {
  release: Release;
  /** Release that keeps serving traffic until cutover. */
  serving: number | null;
}) {
  if (release.status.state !== "in_progress") return null;
  return (
    <section>
      <Heading
        note={`${release.actor.name} · ${changeText(release.change)} · started ${clockPrecise(release.createdAt).slice(0, 8)}`}
      >
        r{release.number} in progress
      </Heading>
      <div className="grid grid-cols-[16px_110px_60px_1fr] items-baseline gap-x-2.5 gap-y-1">
        {release.status.steps.map((step) => {
          const pending = step.state === "pending";
          const text =
            step.state === "running" ? "" : pending ? "text-faint" : "text-dim";
          return (
            <div key={step.name} className="contents">
              <Glyph tone={stepTone[step.state]} hollow={pending} />
              <span className={pending ? "text-dim" : undefined}>
                {stepLabel(step.name)}
              </span>
              <span className={`font-mono text-xs ${text}`}>
                {step.durationMs === null ? "-" : duration(step.durationMs)}
              </span>
              <span className={`truncate font-mono text-xs ${text}`}>
                {step.detail}
              </span>
            </div>
          );
        })}
      </div>
      {serving !== null && (
        <p className="pt-2 text-xs text-dim">
          r{serving} keeps serving until cutover. If the check fails, r
          {release.number} is stopped.
        </p>
      )}
    </section>
  );
}
