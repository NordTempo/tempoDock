import type {
  EnvironmentView,
  ReleaseSummary,
  ServiceView,
} from "../../data/types";

// A project as a pipeline (ADR 0099): its environments in the order a
// release goes through them (previews apart, then the others, production
// last), and for each service what lies between two of them: the same
// image, a newer one to promote, a promote that failed, or settings that
// differ. The Projects page's grid and a service's promote line
// both read it; everything comes from the project overview, nothing more
// is fetched.

/**
 * A project's environments in pipeline order: `previews` (one per pull
 * request, ADR 0056) apart, and `lanes`, the others with production last.
 */
export function stages(envs: readonly EnvironmentView[]) {
  const previews = envs.filter((e) => e.environment.preview);
  const rest = envs.filter((e) => !e.environment.preview);
  const lanes = [
    ...rest.filter((e) => e.environment.kind !== "production"),
    ...rest.filter((e) => e.environment.kind === "production"),
  ];
  return { previews, lanes };
}

/** One setting that differs: its name and the value on each side. */
export type SettingDiff = { label: string; from: string; to: string };

/**
 * The settings that make the same image behave differently, as people
 * name them: port, memory, CPUs, start command and health check path.
 * Effective limits, so "the default" compares by what it is.
 */
const settings: [string, (s: ServiceView["service"]) => string][] = [
  ["port", (s) => String(s.port || 8080)],
  ["memory", (s) => `${s.effective_memory_mb} MB`],
  ["CPUs", (s) => String(s.effective_cpus)],
  ["start command", (s) => s.start_command || "the image's"],
  ["health check path", (s) => s.health_check_path || "/"],
];

/** The settings `from` and `to` set differently, in `settings`' order. */
export function settingDiffs(from: ServiceView, to: ServiceView) {
  const diffs: SettingDiff[] = [];
  for (const [label, value] of settings) {
    const a = value(from.service);
    const b = value(to.service);
    if (a !== b) diffs.push({ label, from: a, to: b });
  }
  return diffs;
}

/**
 * The failed deploy that is still the news on a service: the overview's
 * last_failed_deploy (it survives a rollback and clears once a new image
 * or config deploys), else the latest release when it failed and nothing
 * newer serves. Once more releases came after it, it's history.
 */
export function failedNews(view: ServiceView): ReleaseSummary | undefined {
  const serving = view.service.active_release;
  const latest = view.latest_release;
  if (view.deploying) return undefined;
  const failed =
    view.last_failed_deploy ??
    (latest?.status === "failed" && latest.number > (serving ?? 0)
      ? latest
      : undefined);
  return failed &&
    serving !== failed.number &&
    (serving ?? 0) <= failed.number + 1
    ? failed
    : undefined;
}

/**
 * What lies between one service in two environments, `from` the earlier
 * one (staging) and `to` the later (production):
 * - failed: `to`'s failed deploy (still the news) tried the image `from`
 *   serves, so that image failed there;
 * - ahead: `from` serves a newer image than `to`, ready to promote;
 * - behind: `from` serves an older one (production got a fix first);
 * - same: the same image.
 * `diffs` are the settings that differ either way. Undefined when the
 * service isn't serving on both sides.
 */
export type Step =
  | { kind: "failed"; release: ReleaseSummary; diffs: SettingDiff[] }
  | { kind: "ahead" | "behind" | "same"; diffs: SettingDiff[] };

/** The Step from one environment's service to the next one's. */
export function step(
  from: ServiceView | undefined,
  to: ServiceView | undefined,
): Step | undefined {
  const a = from?.active_release;
  if (!from || !to || !a) return undefined;
  const diffs = settingDiffs(from, to);
  const failed = failedNews(to);
  if (failed && failed.image_id === a.image_id) {
    return { kind: "failed", release: failed, diffs };
  }
  const b = to.active_release;
  if (!b) return { kind: "ahead", diffs };
  if (a.image_id === b.image_id) return { kind: "same", diffs };
  return { kind: a.created_at > b.created_at ? "ahead" : "behind", diffs };
}

/**
 * The other environment that serves the image a failed release tried,
 * and how its settings differ from this one's: the "works in staging"
 * line on a failed deploy. Pipeline order, the nearest lane first.
 */
export function servedElsewhere(
  envs: readonly EnvironmentView[],
  here: ServiceView,
  failed: ReleaseSummary,
) {
  const { lanes } = stages(envs);
  for (const e of lanes) {
    if (e.environment.name === here.service.environment) continue;
    const there = e.services.find((s) => s.service.name === here.service.name);
    if (there?.active_release?.image_id === failed.image_id) {
      return { view: there, diffs: settingDiffs(there, here) };
    }
  }
  return undefined;
}
