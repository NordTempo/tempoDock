import { useSyncExternalStore } from "react";
import type { EnvRef } from "../../data/types";

// "Lock <env>" from the palette opens the environment's lock form on the
// project page. Kept apart from the overview's components so the palette
// (in the shell chunk) imports only this.

let wanted: EnvRef | null = null;
const listeners = new Set<() => void>();

/** Asks the environment's section to open its lock form. */
export function requestLock(env: EnvRef) {
  wanted = env;
  for (const l of listeners) l();
}

/** Clears the request once the form has opened. */
export function lockRequestDone() {
  wanted = null;
  for (const l of listeners) l();
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** Whether the palette asked to lock this environment. */
export const useLockRequested = (env: EnvRef) =>
  useSyncExternalStore(
    subscribe,
    () =>
      wanted?.project === env.project && wanted.environment === env.environment,
  );
