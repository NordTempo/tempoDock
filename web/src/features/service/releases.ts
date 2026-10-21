import type { Release } from "../../data/types";

/** The release a one-click rollback goes back to: the newest replaced one. */
export const rollbackTarget = (releases: Release[]) =>
  releases.find((r) => r.status === "superseded");
