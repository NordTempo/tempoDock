/**
 * `url` as an `href` when it is an https address, else undefined: an `<a>`
 * without an href is plain text. For a link whose address the API sends
 * (a pull request's page), so nothing but a web page is ever opened.
 */
export function httpsHref(url: string | undefined): string | undefined {
  return url && URL.canParse(url) && new URL(url).protocol === "https:"
    ? url
    : undefined;
}
