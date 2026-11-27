import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { refetchedOnSettle } from "../../data/action";
import type { Release, ServiceRef } from "../../data/types";
import { type DeployQueue, withRelease } from "./queue";

// The deploy queue (ADR 0086, amended 2026-11-27): per builder, the
// release it builds and the ones waiting behind it, then the deploys past
// their build. The org's and the console's. Kept live from events
// (applyQueueEvent, applyQueueRelease, from data/live.ts); nothing polls.
// In the initial bundle with live.ts, so it holds keys, queries and the
// events only.

export type { DeployQueue, QueueBuilder, QueueItem } from "./queue";

export const queueKeys = {
  all: ["deploy-queue"] as const,
  /** The org's queue, or every org's in the console. */
  queue: (console: boolean) => ["deploy-queue", console] as const,
};

export const queueQuery = (console: boolean) =>
  queryOptions({
    queryKey: queueKeys.queue(console),
    queryFn: () =>
      body(
        console
          ? api.GET("/api/v1/console/deploy-queue")
          : api.GET("/api/v1/deploy-queue"),
      ),
  });

/**
 * A deploy_queue event: a builder's line moved. It carries nothing, so
 * the queues read again, only while a page shows them.
 */
export function applyQueueEvent(queryClient: QueryClient) {
  // A cancel from the panel refetches its own queue once it ends.
  queryClient.invalidateQueries({
    queryKey: queueKeys.all,
    predicate: (q) => !refetchedOnSettle(q.queryKey),
  });
}

/**
 * A release event of the org's: its item in the org's queue takes its
 * new step, and leaves once done. A release not in the cache yet (just
 * queued), or past its build while still at a builder, reads the queue
 * again, while it is shown.
 */
export function applyQueueRelease(
  queryClient: QueryClient,
  ref: ServiceRef,
  release: Release,
) {
  const key = queueKeys.queue(false);
  const queue = queryClient.getQueryData<DeployQueue>(key);
  if (!queue) return;
  const next = withRelease(queue, ref, release);
  if (next === "refetch") applyQueueEvent(queryClient);
  else if (next) queryClient.setQueryData<DeployQueue>(key, next);
}
