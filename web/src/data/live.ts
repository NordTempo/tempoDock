import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { subscribe } from "./fixtures";
import { keys } from "./queries";
import type { LiveEvent, LogLine } from "./types";

/** Lines kept per service in the cache; older ones fall off. */
export const LOG_CAP = 2000;

/**
 * Applies one live event to the query cache. Log lines are patched in place
 * (they arrive often); everything else invalidates the affected subtree and
 * lets the active queries refetch.
 */
export function applyLiveEvent(queryClient: QueryClient, event: LiveEvent) {
  switch (event.type) {
    case "log.line":
      queryClient.setQueryData<LogLine[]>(
        keys.logs(event.serviceId),
        (lines) => (lines ? [event.line, ...lines].slice(0, LOG_CAP) : lines),
      );
      return;
    case "service.updated":
    case "domain.updated":
      queryClient.invalidateQueries({
        queryKey: keys.service(event.serviceId),
        // Logs have their own event; refetching them here would be waste.
        predicate: (query) => query.queryKey[2] !== "logs",
      });
      queryClient.invalidateQueries({ queryKey: keys.projects });
      queryClient.invalidateQueries({ queryKey: ["audit"] });
      return;
    case "server.updated":
      queryClient.invalidateQueries({ queryKey: keys.servers });
      queryClient.invalidateQueries({ queryKey: keys.projects });
      return;
  }
}

/**
 * Keeps the cache live while the app shell is mounted.
 *
 * Today the events come from the fixture backend. Once the daemon streams
 * them, replace the effect with the SSE hook from src/api/events.ts:
 *
 *   useServerEvents({
 *     "log.line": (data, qc) => applyLiveEvent(qc, parseLogLine(data)),
 *     "service.updated": (data, qc) => applyLiveEvent(qc, parseService(data)),
 *     ...
 *   });
 *
 * where each parse narrows the `unknown` payload to its LiveEvent member.
 */
export function useLiveUpdates() {
  const queryClient = useQueryClient();
  useEffect(
    () => subscribe((event) => applyLiveEvent(queryClient, event)),
    [queryClient],
  );
}
