import { useSyncExternalStore } from "react";

/**
 * The live event stream's health, for the shell's "not live" marker:
 * live, dropped (the browser is retrying), or closed (it gave up).
 */
export type LiveStatus = "live" | "dropped" | "closed";

let status: LiveStatus = "live";
// Bumped by Reconnect: part of the stream's URL, so a new one opens.
let attempt = 0;
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

/** Called by the live stream (data/live.ts) as its connection changes. */
export function setLiveStatus(next: LiveStatus) {
  if (next === status) return;
  status = next;
  emit();
}

/** Opens a fresh live stream after it closed for good. */
export function reconnectLive() {
  attempt++;
  emit();
}

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

/** The current status, outside React. */
export const liveStatus = () => status;

export const useLiveStatus = () => useSyncExternalStore(subscribe, liveStatus);

/** The reconnect counter, for the stream's URL. */
export const useLiveAttempt = () =>
  useSyncExternalStore(subscribe, () => attempt);
