/**
 * A choice remembered on this browser, like a page's time range, under
 * localStorage key `dock.<key>`. get() is the last value set(), or
 * fallback until there is one (or when what was saved is no longer an
 * option, or the browser blocks storage). Pages read it when their URL
 * names none, so ?range= in a shared link still wins, and set() it when
 * the user picks, keeping the pick in the URL too.
 */
export function remembered<T extends string>(
  key: string,
  options: readonly T[],
  fallback: T,
) {
  const storageKey = `dock.${key}`;
  return {
    get: (): T => {
      try {
        const saved = localStorage.getItem(storageKey);
        return options.find((o) => o === saved) ?? fallback;
      } catch {
        return fallback; // storage blocked: the page's default
      }
    },
    set: (value: T) => {
      try {
        localStorage.setItem(storageKey, value);
      } catch {
        // Storage blocked or full: the pick lasts until the page is left.
      }
    },
  };
}
