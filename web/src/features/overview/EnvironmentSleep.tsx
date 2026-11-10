import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { Environment } from "../../data/types";
import { bytes, since } from "../../ui/format";
import { Act, StateText } from "../../ui/kit";

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

/** The CLI equivalent of an environment's sleep action. */
export const sleepCli = (verb: "wake" | "sleep", ref: EnvRef) =>
  `dock env ${verb} ${ref.environment} -p ${ref.project}`;

/**
 * An environment's sleep in its heading: "◌ asleep 2h · 386 MB freed ·
 * wakes on the next request" and Wake, or "◐ waking". Nothing while it
 * is awake.
 */
export function SleepState({ environment }: { environment: Environment }) {
  const wake = useWakeEnvironment();
  const sl = environment.sleep;
  if (!sl || sl.state === "awake") return null;
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
