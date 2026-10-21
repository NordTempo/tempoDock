import { useSyncExternalStore } from "react";
import type { LiveEvent, ServiceRef } from "./types";

// What happened while this tab was open: deploy progress lines and crashes
// from the live event bus. The audit log has who did what; this has what
// the platform did about it. Kept in memory only, newest first.

export type FeedItem = {
  id: string;
  time: string;
  ref: ServiceRef;
  release?: number;
  kind: "progress" | "live" | "failed" | "restarting" | "crashed";
  text: string;
};

const CAP = 60;
let items: FeedItem[] = [];
const listeners = new Set<() => void>();
let seq = 0;

function push(item: Omit<FeedItem, "id">) {
  items = [{ ...item, id: String(seq++) }, ...items].slice(0, CAP);
  for (const listener of listeners) listener();
}

/** Records the events worth showing in the overview's Live column. */
export function recordLiveEvent(event: LiveEvent) {
  const { project, environment, service } = event;
  if (!project || !environment || !service) return;
  const ref = { project, environment, service };
  if (event.type === "release_event" && event.release_event) {
    const { message, release, created_at } = event.release_event;
    const kind = message.endsWith(" is live")
      ? "live"
      : / failed: /.test(message)
        ? "failed"
        : "progress";
    push({ time: created_at, ref, release, kind, text: message });
  }
  if (event.type === "service_status" && event.service_status) {
    const { state, restart_count, exit_code } = event.service_status;
    if (state === "restarting") {
      push({
        time: event.time,
        ref,
        kind: "restarting",
        text: `restart ${restart_count}, exit ${exit_code}`,
      });
    } else if (state === "crashed") {
      push({
        time: event.time,
        ref,
        kind: "crashed",
        text: `crashed, exit ${exit_code}`,
      });
    }
  }
}

/** The feed for one project. */
export function useFeed(project: string) {
  const all = useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => items,
  );
  return all.filter((item) => item.ref.project === project);
}
