import { useEffect, useRef, useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { Environment } from "../../data/types";
import { bytes, since } from "../../ui/format";
import { Act, Button, ErrorText, StateText } from "../../ui/kit";

// Sleeping environments (ADR 0070, mock A): an asleep environment's
// state, how much memory it gave back and Wake, in its heading on the
// project and environment pages. Awake ones say nothing here; their
// services' states tell the rest. Nothing moves: the waking state is a
// static glyph, and each service turns running as its event arrives.

type EnvRef = { project: string; environment: string };

const envPath = (ref: EnvRef) => ({
  params: { path: { project: ref.project, environment: ref.environment } },
});

/** Wakes an asleep environment; answers once it is awake. */
export const useWakeEnvironment = () =>
  useAction(
    (ref: EnvRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/wake",
          envPath(ref),
        ),
      ),
    (ref) => [keys.overview(ref.project)],
    (ref) => `${ref.environment} is awake`,
  );

/** Puts an environment to sleep now. */
export const useSleepEnvironment = () =>
  useAction(
    (ref: EnvRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/sleep",
          envPath(ref),
        ),
      ),
    (ref) => [keys.overview(ref.project)],
    (ref) => `${ref.environment} is asleep`,
  );

/**
 * Changes how long an environment may go without requests before it
 * sleeps (admin; 0 turns it off). Its sleep event patches the overview.
 */
export const useSetSleep = () =>
  useAction(
    ({ ref, minutes }: { ref: EnvRef; minutes: number }) =>
      body(
        api.PUT("/api/v1/projects/{project}/environments/{environment}/sleep", {
          ...envPath(ref),
          body: { after_minutes: minutes },
        }),
      ),
    ({ ref }) => [keys.overview(ref.project)],
    ({ ref, minutes }) =>
      minutes > 0
        ? `${ref.environment} sleeps after ${duration(minutes)} without requests`
        : `${ref.environment} no longer sleeps on its own`,
  );

/** Minutes as people say them, and as `dock env sleep-after` takes them. */
export const duration = (m: number) => (m % 60 === 0 ? `${m / 60}h` : `${m}m`);

/** The CLI equivalent of an environment's sleep action. */
export const sleepCli = (verb: "wake" | "sleep", ref: EnvRef) =>
  `dock env ${verb} ${ref.environment} -p ${ref.project}`;

/**
 * An environment's sleep in its heading: "◌ asleep 2h · 386 MB freed ·
 * wakes on the next request" and Wake, or "◐ waking". Awake, a quiet
 * "sleeps after 2h idle" when it sleeps on its own (with why it can't,
 * when it can't, on hover), else nothing.
 */
export function SleepState({ environment }: { environment: Environment }) {
  const wake = useWakeEnvironment();
  const sl = environment.sleep;
  if (!sl) return null;
  if (sl.state === "awake")
    return sl.after_minutes > 0 ? (
      <span
        className="text-xs text-mut"
        title={sl.blocked ? `Can't sleep now: ${sl.blocked}` : undefined}
      >
        sleeps after {duration(sl.after_minutes)} idle
        {sl.blocked && " (can't now)"}
      </span>
    ) : null;
  const ref = { project: environment.project, environment: environment.name };
  if (sl.state !== "asleep" || wake.isPending)
    return (
      <StateText
        state={{
          tone: "prog",
          glyph: "◐",
          label: sl.state === "sleeping" ? "going to sleep" : "waking",
        }}
      />
    );
  const facts = [
    sl.freed_bytes ? `${bytes(sl.freed_bytes)} freed` : "",
    "wakes on the next request",
  ].filter(Boolean);
  return (
    <>
      <StateText
        state={{
          tone: "info",
          glyph: "◌",
          label: sl.since ? `asleep ${since(sl.since)}` : "asleep",
        }}
      />
      <span className="text-xs text-mut">{facts.join(" · ")}</span>
      <Act small cli={sleepCli("wake", ref)} onClick={() => wake.mutate(ref)}>
        Wake
      </Act>
    </>
  );
}

/** The choices of SleepForm, in minutes; 0 is never. */
const choices = [0, 15, 30, 60, 120, 360, 1440];

/**
 * The one-line form under an environment's heading that sets when it
 * sleeps (mock A): Never, a few common times, or a custom number of
 * minutes (5 to 1440). Admins of non-preview, non-production
 * environments open it from the heading's menu; previews take their
 * base's preview limits.
 */
export function SleepForm({
  environment,
  onDone,
}: {
  environment: Environment;
  onDone: () => void;
}) {
  const set = useSetSleep();
  // Opened from the menu: the choice is the first thing to make.
  const select = useRef<HTMLSelectElement>(null);
  useEffect(() => select.current?.focus(), []);
  const ref = { project: environment.project, environment: environment.name };
  const current = environment.sleep?.after_minutes ?? 0;
  const [pick, setPick] = useState(
    choices.includes(current) ? String(current) : "custom",
  );
  const [custom, setCustom] = useState(String(current || 45));
  const minutes = pick === "custom" ? Number(custom) : Number(pick);
  const valid =
    minutes === 0 ||
    (Number.isInteger(minutes) && minutes >= 5 && minutes <= 1440);
  const cli = `dock env sleep-after ${ref.environment} ${valid && minutes > 0 ? duration(minutes) : "off"} -p ${ref.project}`;
  return (
    <form
      className="mb-2.5 flex flex-wrap items-center gap-x-2.5 gap-y-1.5 rounded-ctl border border-line bg-panel px-3 py-1.5 text-xs"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) set.mutate({ ref, minutes }, { onSuccess: onDone });
      }}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
    >
      <label htmlFor={`sleep-${environment.id}`}>Sleep after</label>
      <select
        id={`sleep-${environment.id}`}
        ref={select}
        value={pick}
        onChange={(e) => setPick(e.target.value)}
        className="field py-[3px] font-mono text-xs"
      >
        {choices.map((m) => (
          <option key={m} value={String(m)}>
            {m === 0 ? "Never" : duration(m)}
          </option>
        ))}
        <option value="custom">Custom</option>
      </select>
      {pick === "custom" && (
        <input
          aria-label="Minutes"
          type="number"
          min={5}
          max={1440}
          value={custom}
          onChange={(e) => setCustom(e.target.value)}
          className="field w-16 py-0.5 font-mono text-xs"
        />
      )}
      {minutes !== 0 && <span className="text-mut">without requests</span>}
      <Act
        small
        primary
        type="submit"
        cli={cli}
        disabled={set.isPending || !valid}
      >
        Save
      </Act>
      <Button small onClick={onDone}>
        Cancel
      </Button>
      <span className="text-mut">
        {valid
          ? "Its services stop and the next request wakes it."
          : "Pick 5 to 1440 minutes."}
      </span>
      <ErrorText error={set.error} />
    </form>
  );
}
