/**
 * A choice remembered on this browser, like a page's time range, under
 * localStorage key `dock.<key>`. get() is the last value set(), or
 * fallback until there is one (or when what was saved is no longer an
 * option). Pages read it when their URL names none, so ?range= in a
 * shared link still wins, and set() it when the user picks.
 */
export function remembered<T extends string>(
  key: string,
  options: readonly T[],
  fallback: T,
) {
  const storageKey = `dock.${key}`;
  return {
    get: (): T => {
      const saved = localStorage.getItem(storageKey);
      return options.find((o) => o === saved) ?? fallback;
    },
    set: (value: T) => localStorage.setItem(storageKey, value),
  };
}
