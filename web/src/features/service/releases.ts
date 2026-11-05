import type { Release, ReleaseSummary } from "../../data/types";
import { shortId } from "../../ui/status";

/** The release a one-click rollback goes back to: the newest replaced one. */
export function rollbackTarget(releases: Release[]) {
  // The serving release may itself be a rollback to the newest superseded
  // one; rolling back to that again would change nothing, so skip it.
  const active = releases.find((r) => r.status === "active");
  return releases.find(
    (r) =>
      r.status === "superseded" &&
      !(active?.from_release === r.number && active.image_id === r.image_id),
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
