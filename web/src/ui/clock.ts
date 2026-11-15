import { useSyncExternalStore } from "react";

// One shared 1-second clock for durations that count while something runs
// (a deploy step's time). It ticks only while a component asks for it with
// `active` and the tab is visible: with nothing running on screen, or the
// tab hidden, no timer is armed. Each tick changes text only.

let now = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const listeners = new Set<() => void>();

const tick = () => {
  now = Date.now();
  for (const listener of listeners) listener();
};

/** Arms or clears the timer to match who listens and whether the tab shows. */
const sync = () => {
  const run = listeners.size > 0 && document.visibilityState === "visible";
  if (run && !timer) {
    tick();
    timer = setInterval(tick, 1000);
  } else if (!run && timer) {
    clearInterval(timer);
    timer = undefined;
  }
};

const subscribe = (listener: () => void) => {
  if (listeners.size === 0) document.addEventListener("visibilitychange", sync);
  listeners.add(listener);
  sync();
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0)
      document.removeEventListener("visibilitychange", sync);
    sync();
  };
};

const idle = () => () => {};

// Between ticks the value is fixed, as useSyncExternalStore needs; with
// no timer armed it catches up to the wall clock at most once a second.
const snapshot = () => {
  if (!timer && Date.now() - now >= 1000) now = Date.now();
  return now;
};

/**
 * The current time in ms, re-rendering every second while `active` (say,
 * while a release is in flight). Inactive callers get a value that doesn't
 * re-render them.
 */
export const useNow = (active: boolean) =>
  useSyncExternalStore(active ? subscribe : idle, snapshot);
