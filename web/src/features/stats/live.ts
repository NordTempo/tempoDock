import { useQueryClient } from "@tanstack/react-query";
import { useState, useSyncExternalStore } from "react";
import { useEventStream } from "../../api/events";
import { patchService } from "../../data/live";
import { keys } from "../../data/queries";
import type {
  ServerDetail,
  ServerUsage,
  ServiceRef,
  ServiceUsage,
} from "../../data/types";

// Live statistics: while a server or service page is open and the browser
// tab is visible, its `stats/live` stream patches the latest sample into
// the query cache (every few seconds). The daemon samples only while
// someone watches, so hiding the tab stops the sampling too.

const onVisibility = (notify: () => void) => {
  document.addEventListener("visibilitychange", notify);
  return () => document.removeEventListener("visibilitychange", notify);
};

/** True while the browser tab is visible. */
export const usePageVisible = () =>
  useSyncExternalStore(
    onVisibility,
    () => document.visibilityState === "visible",
  );

/** "live", "paused" (tab hidden) or "lost" (the stream failed). */
export type LiveState = "live" | "paused" | "lost";

function useLiveStream<T>(url: string, onSample: (sample: T) => void) {
  const visible = usePageVisible();
  const [lost, setLost] = useState<string | null>(null);
  useEventStream<T>(
    visible && !lost ? url : null,
    ["sample"],
    (_, sample) => onSample(sample),
    { onClose: (error) => setLost(error ?? "stream ended") },
  );
  const state: LiveState = lost ? "lost" : visible ? "live" : "paused";
  return state;
}

/** Keeps a server's latest sample (`server.usage` of its detail) live. */
export function useLiveServer(name: string) {
  const queryClient = useQueryClient();
  return useLiveStream<ServerUsage>(
    `/api/v1/servers/${encodeURIComponent(name)}/stats/live`,
    (usage) =>
      queryClient.setQueryData<ServerDetail>(
        keys.server(name),
        (d) => d && { ...d, server: { ...d.server, usage } },
      ),
  );
}

/** Keeps a service's `usage` in its project overview live. */
export function useLiveService(ref: ServiceRef) {
  const queryClient = useQueryClient();
  const path = [ref.project, ref.environment, ref.service].map(
    encodeURIComponent,
  );
  return useLiveStream<ServiceUsage>(
    `/api/v1/projects/${path[0]}/environments/${path[1]}/services/${path[2]}/stats/live`,
    (usage) =>
      patchService(queryClient, ref, (s) => ({
        ...s,
        service: { ...s.service, usage },
      })),
  );
}
