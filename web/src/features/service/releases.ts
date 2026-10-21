import type { Release } from "../../data/types";
import { shortId } from "../../ui/status";

/** The release a one-click rollback goes back to: the newest replaced one. */
export const rollbackTarget = (releases: Release[]) =>
  releases.find((r) => r.status === "superseded");

/**
 * What a rollback to `r` brings back, since it copies the image and the
 * resolved config: "app:1 4be1c0ffee12 · port 8080 · 7 variables".
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
