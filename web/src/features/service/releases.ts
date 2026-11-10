import type { Release, ReleaseSummary, Service } from "../../data/types";
import { shortId } from "../../ui/status";

/**
 * The serving release already runs `r`'s image and config: it is a
 * rollback to `r` (or a restart of one). Rolling back to `r` would change
 * nothing, so no screen offers it.
 */
export const servesCopyOf = (serving: Release | undefined, r: Release) =>
  serving?.from_release === r.number && serving.image_id === r.image_id;

/** The release a one-click rollback goes back to: the newest replaced one. */
export function rollbackTarget(releases: Release[]) {
  const active = releases.find((r) => r.status === "active");
  return releases.find(
    (r) => r.status === "superseded" && !servesCopyOf(active, r),
  );
}

/**
 * What a rollback to `r` brings back, since it copies the image and the
 * resolved config: "app:1 4be1c0f · port 8080 · 7 variables".
 */
export function copiedConfig(r: Release) {
  const { port, variable_keys, mounts } = r.config;
  const vars = variable_keys?.length ?? 0;
  return [
    `${r.image_ref} ${shortId(r.image_id)}`.trim(),
    `port ${port}`,
    `${vars} ${vars === 1 ? "variable" : "variables"}`,
    ...(mounts ?? []).map((m) => `${m.volume} at ${m.path}`),
  ].join(" · ");
}

/**
 * The most specific part of a release error, which nests causes with
 * colons: "… timed out after 1m0s: GET /health returned 404 Not Found"
 * becomes "GET /health returned 404 Not Found".
 */
export function errorSummary(r: Pick<ReleaseSummary, "error">) {
  const error = r.error ?? "";
  const last = error.split(": ").at(-1) ?? error;
  // A cause cut out of "(last: …)" keeps its closing bracket; drop it.
  const open = last.split("(").length;
  const close = last.split(")").length;
  return close > open ? last.replace(/\)+$/, "") : last;
}

/**
 * An image as people say it: the last path part and its tag, a digest
 * cut to 7 characters ("shop-web:1.3.0", "redis:8-alpine@615b4ce").
 */
export function shortImage(ref: string) {
  const [name = "", digest] = ref.split("@sha256:");
  const last = name.split("/").at(-1) ?? name;
  return digest ? `${last}@${digest.slice(0, 7)}` : last;
}

/** The image a release runs, as short as it stays recognizable. */
export function imageLabel(r: Pick<ReleaseSummary, "image_ref" | "image_id">) {
  if (r.image_ref && !r.image_ref.startsWith("sha256:")) {
    return r.image_ref.replace(/^docker\.io\/(library\/)?/, "");
  }
  return r.image_id ? shortId(r.image_id) : "";
}

/** An image reference without Docker Hub's implied prefix, for comparing. */
const bare = (ref: string) => ref.replace(/^docker\.io\/(library\/)?/, "");

/**
 * The image setting next to what serves. The setting is what the next
 * image deploy (Deploy, `dock deploy` without --image) runs; config
 * changes and rollbacks keep the serving image. They differ after a
 * failed deploy of a new tag, or a rollback. `failed` is the newest
 * release that tried the setting's image, when it failed.
 */
export function imageSetting(service: Service, releases: Release[]) {
  const serving = releases.find((r) => r.status === "active");
  const next = service.image ? bare(service.image) : "";
  const now = serving ? imageLabel(serving) : "";
  const lastTry = next
    ? releases.find((r) => bare(r.image_ref) === next)
    : undefined;
  return {
    serving,
    /** The image that serves now, as imageLabel shows it. */
    now,
    /** The image setting, without docker.io/. */
    next,
    /** The setting differs from what serves. */
    differs: !!next && !!serving && next !== now,
    failed: lastTry?.status === "failed" ? lastTry : undefined,
  };
}

/** One difference between two releases, in words: "port 3000 → 8080". */
export type ReleaseChange = {
  what:
    | "image"
    | "added"
    | "removed"
    | "port"
    | "start command"
    | "health check"
    | "limits"
    | "volumes";
  text: string;
};

/**
 * What differs between release `from` and release `to`: the image,
 * variables added and removed (by name; values are sealed), the port,
 * the start command, the health check path, the limits and the volumes
 * mounted. Empty when they match.
 */
export function releaseDiff(from: Release, to: Release): ReleaseChange[] {
  const before = new Set(from.config.variable_keys ?? []);
  const after = new Set(to.config.variable_keys ?? []);
  const added = [...after].filter((k) => !before.has(k));
  const removed = [...before].filter((k) => !after.has(k));
  const mounts = (r: Release) =>
    (r.config.mounts ?? []).map((m) => `${m.volume} at ${m.path}`).join(", ");
  const out: ReleaseChange[] = [];
  if (from.image_id !== to.image_id) {
    const a = imageLabel(from);
    const b = imageLabel(to);
    out.push({
      what: "image",
      text: `image ${a} → ${b}${a === b ? " (new digest)" : ""}`,
    });
  }
  if (added.length) out.push({ what: "added", text: added.join(", ") });
  if (removed.length) out.push({ what: "removed", text: removed.join(", ") });
  if (from.config.port !== to.config.port) {
    out.push({
      what: "port",
      text: `port ${from.config.port} → ${to.config.port}`,
    });
  }
  if (from.config.start_command !== to.config.start_command) {
    out.push({
      what: "start command",
      text: `start command ${from.config.start_command || "image default"} → ${to.config.start_command || "image default"}`,
    });
  }
  if (from.config.health_check_path !== to.config.health_check_path) {
    out.push({
      what: "health check",
      text: `health check ${from.config.health_check_path || "TCP"} → ${to.config.health_check_path || "TCP"}`,
    });
  }
  if (
    from.config.cpus !== to.config.cpus ||
    from.config.memory_mb !== to.config.memory_mb
  ) {
    const limits = (r: Release) =>
      `${r.config.cpus || "no"} CPU, ${r.config.memory_mb ? `${r.config.memory_mb}M` : "no memory limit"}`;
    out.push({
      what: "limits",
      text: `limits ${limits(from)} → ${limits(to)}`,
    });
  }
  if (mounts(from) !== mounts(to)) {
    out.push({
      what: "volumes",
      text: `volumes ${mounts(from) || "none"} → ${mounts(to) || "none"}`,
    });
  }
  return out;
}

/**
 * What rolling back from `from` to `to` takes away that the app may rely
 * on, one plain sentence each: volumes no longer mounted (or mounted
 * elsewhere) and variables removed. The rollback confirm shows them in
 * warning colour.
 */
export function rollbackLosses(from: Release, to: Release): string[] {
  const out: string[] = [];
  const target = new Map(
    (to.config.mounts ?? []).map((m) => [m.volume, m.path]),
  );
  for (const m of from.config.mounts ?? []) {
    const path = target.get(m.volume);
    if (path === undefined) {
      out.push(
        `${m.volume} is no longer mounted at ${m.path}: the app won't see the files on it. They stay on the volume.`,
      );
    } else if (path !== m.path) {
      out.push(
        `${m.volume} moves from ${m.path} to ${path}: the app won't find its files at ${m.path}.`,
      );
    }
  }
  const after = new Set(to.config.variable_keys ?? []);
  const removed = (from.config.variable_keys ?? []).filter(
    (k) => !after.has(k),
  );
  if (removed.length === 1) {
    out.push(`${removed[0]} is removed: the app won't get this variable.`);
  } else if (removed.length > 1) {
    out.push(
      `${removed.join(", ")} are removed: the app won't get these variables.`,
    );
  }
  return out;
}
