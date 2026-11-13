import { lazy, Suspense, useEffect, useSyncExternalStore } from "react";
import { isTyping, useKeydown } from "../../shell/keys";
import type { TopicId } from "./topics";

/**
 * The help panel's state, outside React so the top bar, the palette, the
 * account menu and "What's this?" links open it without a provider.
 * `pick` is a topic chosen in the panel or by a link, kept while the page
 * it was chosen on is open; otherwise the panel shows the page's topic.
 */
export type HelpState = {
  open: boolean;
  pick: { topic: TopicId; path: string } | null;
};

let state: HelpState = { open: false, pick: null };
const listeners = new Set<() => void>();

function set(next: HelpState) {
  state = next;
  for (const listener of listeners) listener();
}

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

/** Opens help on `topic`, or on the current page's topic. */
export const openHelp = (topic?: TopicId) =>
  set({
    open: true,
    pick: topic ? { topic, path: window.location.pathname } : null,
  });

export const closeHelp = () => set({ open: false, pick: null });

/** Whether the help panel is open, for the top bar's ? button. */
export const useHelpOpen = () =>
  useSyncExternalStore(subscribe, () => state.open);

// The panel and every topic load on the first ?, not with the shell.
const HelpPanel = lazy(() => import("./HelpPanel"));

/**
 * Mounts the help panel while open; ? anywhere outside text fields
 * toggles it. The shell mounts it once.
 */
export function HelpHost() {
  const current = useSyncExternalStore(subscribe, () => state);
  useKeydown((event) => {
    if (event.key !== "?" || isTyping(event) || event.defaultPrevented) return;
    if (document.querySelector("dialog:modal")) return;
    event.preventDefault();
    if (state.open) closeHelp();
    else openHelp();
  });
  // Wide windows make room for the panel instead of covering the page.
  useEffect(() => {
    document.documentElement.classList.toggle("help-open", current.open);
  }, [current.open]);
  if (!current.open) return null;
  return (
    <Suspense fallback={null}>
      <HelpPanel pick={current.pick} />
    </Suspense>
  );
}
