/**
 * Addresses the page links to. The console is Dock itself; the site's
 * Caddyfile sends every path it doesn't serve there too, so links to the
 * console's old address (dock.nordtempo.com) keep working.
 */
export const site = {
  consoleUrl: "https://console.nordtempo.com",
  earlyAccessEmail: "dock@nordtempo.com",
} as const;

/** A mailto link for early access requests, with the subject filled in. */
export const earlyAccessHref = `mailto:${site.earlyAccessEmail}?subject=${encodeURIComponent("Tempo Dock early access")}`;
