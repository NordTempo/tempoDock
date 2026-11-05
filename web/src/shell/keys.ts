import { useEffect, useEffectEvent, useSyncExternalStore } from "react";

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
export const modKey = isMac ? "⌘" : "Ctrl ";

/** True while focus is in a text field, where single-key shortcuts must not fire. */
export function isTyping(event: KeyboardEvent) {
  const el = event.target;
  return (
    el instanceof HTMLElement &&
    (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName))
  );
}

/** Window keydown listener for the lifetime of the component. */
export function useKeydown(handler: (event: KeyboardEvent) => void) {
  const onKey = useEffectEvent(handler);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => onKey(event);
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);
}

/**
 * A page's keyboard shortcut. `key` is one key ("d", "1", "?") or a
 * g-sequence ("g l": press g, then l). Shortcuts with a `label` show in the
 * command line legend and the ? help.
 */
export type Shortcut = {
  key: string;
  label?: string;
  run: () => void;
  /** Listed, but does nothing (e.g. no earlier release to roll back to). */
  disabled?: boolean;
};

export type LegendItem = { key: string; label: string };

// Every mounted page's labelled shortcuts, for the legend and ? help.
const legends = new Map<symbol, LegendItem[]>();
let legendSnapshot: LegendItem[] = [];
const listeners = new Set<() => void>();

function publish() {
  legendSnapshot = [...legends.values()].flat();
  for (const listener of listeners) listener();
}

/** The labelled shortcuts of everything on screen. */
export const useLegend = () =>
  useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => legendSnapshot,
  );

// A g-sequence is waiting for its second key until this time.
let sequenceUntil = 0;

/**
 * Binds single-key shortcuts while mounted. They never fire while typing,
 * with Cmd, Ctrl or Alt held, or behind an open modal (a confirmation).
 */
export function useShortcuts(shortcuts: Shortcut[]) {
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || isTyping(event)) return;
    if (document.querySelector("dialog:modal")) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    const key = event.key.toLowerCase();
    const inSequence = Date.now() < sequenceUntil;
    const wanted = inSequence ? `g ${key}` : key;
    const hit = shortcuts.find((s) => s.key === wanted);
    if (hit) {
      sequenceUntil = 0;
      event.preventDefault();
      if (!hit.disabled) hit.run();
      return;
    }
    if (
      !inSequence &&
      key === "g" &&
      shortcuts.some((s) => s.key.startsWith("g "))
    ) {
      sequenceUntil = Date.now() + 1200;
    }
  });

  const signature = shortcuts
    .filter((s) => s.label)
    .map((s) => `${s.key}\u0000${s.label}`)
    .join("\u0001");

  useEffect(() => {
    const listener = (event: KeyboardEvent) => onKey(event);
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);

  useEffect(() => {
    if (!signature) return;
    const id = Symbol();
    legends.set(
      id,
      signature.split("\u0001").map((entry) => {
        const [key = "", label = ""] = entry.split("\u0000");
        return { key, label };
      }),
    );
    publish();
    return () => {
      legends.delete(id);
      publish();
    };
  }, [signature]);
}
