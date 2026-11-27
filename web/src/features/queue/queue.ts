import type { components } from "../../api/schema";
import type { Release, ServiceRef } from "../../data/types";

// The deploy queue's pure parts: how a release event moves its item, and
// what the top bar counts. Apart from data.ts so node:test runs them.

type Schemas = components["schemas"];
export type DeployQueue = Schemas["DeployQueue"];
export type QueueBuilder = Schemas["QueueBuilder"];
export type QueueItem = Schemas["QueueItem"];

type Step = NonNullable<QueueItem["status"]>;

/** Whether a release's status is a step of a deploy in flight. */
const inFlight = (s: Release["status"]): s is Step =>
  s === "queued" ||
  s === "building" ||
  s === "starting" ||
  s === "health_checking";

/**
 * The queue with a release's new status: its item steps on or leaves,
 * "refetch" when only the server can place it, undefined when it isn't
 * the queue's (done and not in it).
 */
export function withRelease(
  queue: DeployQueue,
  ref: ServiceRef,
  release: Pick<Release, "number" | "status">,
): DeployQueue | "refetch" | undefined {
  const mine = (it: QueueItem) =>
    !it.hidden &&
    it.project === ref.project &&
    it.environment === ref.environment &&
    it.service === ref.service &&
    it.release === release.number;
  const deploying = queue.deploying ?? [];
  const builders = queue.builders ?? [];
  const status = release.status;
  const done = !inFlight(status);
  const inQueue = queueItems(queue).some(mine);
  if (!inQueue) return done ? undefined : "refetch";
  // A step past its build leaves the builder; reading again places it.
  if (!done && status !== "building" && !deploying.some(mine)) return "refetch";
  const step = (it: QueueItem): QueueItem =>
    mine(it) && inFlight(status) ? { ...it, status } : it;
  const keep = (it: QueueItem) => !(done && mine(it));
  return {
    deploying: deploying.filter(keep).map(step),
    builders: builders.map((b) => ({
      ...b,
      building: b.building && keep(b.building) ? step(b.building) : undefined,
      waiting: (b.waiting ?? []).filter(keep).map(step),
    })),
  };
}

/** Every item of the queue, at builders and past them. */
const queueItems = (queue: DeployQueue): QueueItem[] => [
  ...(queue.deploying ?? []),
  ...(queue.builders ?? []).flatMap((b) => [
    ...(b.building ? [b.building] : []),
    ...(b.waiting ?? []),
  ]),
];

/**
 * What the top bar counts: the reader's own releases deploying (in a
 * builder's slot or past it) and waiting (in a builder's line, or queued
 * before one). Hidden ones aren't counted.
 */
export function queueCount(queue: DeployQueue | undefined) {
  let active = 0;
  let waiting = 0;
  if (!queue) return { active, waiting };
  for (const b of queue.builders ?? []) {
    if (b.building && !b.building.hidden) active++;
    waiting += (b.waiting ?? []).filter((it) => !it.hidden).length;
  }
  for (const it of queue.deploying ?? []) {
    if (it.status === "queued") waiting++;
    else active++;
  }
  return { active, waiting };
}
