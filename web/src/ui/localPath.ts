/**
 * `to` when it is a path on this origin, else undefined: where a sign-in
 * page may send the browser on to, from a `redirect` anyone can put in a
 * link. Browsers read a backslash as a slash and drop tabs and newlines
 * from a URL, so "/\host" or "/\t/host" would become "//host", another
 * site: any of them, or any other control character, is refused. The
 * same rule as the server's (`access.LocalPath`).
 */
export function localPath(to: unknown): string | undefined {
  if (typeof to !== "string" || !to.startsWith("/") || to.startsWith("//")) {
    return undefined;
  }
  for (let i = 0; i < to.length; i++) {
    const c = to.charCodeAt(i);
    if (c < 0x20 || c === 0x7f || c === 0x5c) return undefined;
  }
  return to;
}
