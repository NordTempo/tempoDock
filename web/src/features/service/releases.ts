import type { Release, Service } from "../../data/types";

/** The release a one-click rollback goes back to: newest restorable one. */
export function rollbackTarget(releases: Release[]) {
  return releases.find(
    (r) => r.status.state === "superseded" && !r.status.imagePruned,
  );
}

/** The release currently moving through deploy steps, if any. */
export function inFlightRelease(service: Service, releases: Release[]) {
  if (service.state.state !== "deploying") return undefined;
  const number = service.state.release;
  return releases.find((r) => r.number === number);
}

/** "release r90", plus "after r89 finishes" while a deploy is in flight. */
export function nextRelease(service: Service, releases: Release[]) {
  const next = Math.max(0, ...releases.map((r) => r.number)) + 1;
  const after =
    service.state.state === "deploying"
      ? ` after r${service.state.release} finishes`
      : "";
  return `release r${next}${after}`;
}

/** What applying staged changes will do to this service, in one sentence. */
export const applyNote = (service: Service, releases: Release[]) =>
  `Creates ${nextRelease(service, releases)}. Health-checked before traffic.`;
