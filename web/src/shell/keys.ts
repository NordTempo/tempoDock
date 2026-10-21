import { useEffect, useEffectEvent } from "react";

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

/** Single-key shortcut, ignored while typing or with modifiers held. */
export function useShortcut(key: string, action: () => void) {
  useKeydown((event) => {
    if (event.key !== key || isTyping(event)) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    event.preventDefault();
    action();
  });
}
