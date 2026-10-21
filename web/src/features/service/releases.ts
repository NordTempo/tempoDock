import type { Release, ReleaseSummary } from "../../data/types";
import { shortId } from "../../ui/status";

/** The release a one-click rollback goes back to: the newest replaced one. */
export const rollbackTarget = (releases: Release[]) =>
  releases.find((r) => r.status === "superseded");

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
  return error.split(": ").at(-1) ?? error;
}

/** The image a release runs, as short as it stays recognizable. */
export function imageLabel(r: Pick<ReleaseSummary, "image_ref" | "image_id">) {
  if (r.image_ref && !r.image_ref.startsWith("sha256:")) {
    return r.image_ref.replace(/^docker\.io\/(library\/)?/, "");
  }
  return r.image_id ? shortId(r.image_id) : "";
}
