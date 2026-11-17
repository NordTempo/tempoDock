/**
 * Addresses the page links to. The console is Dock itself; the site's
 * Caddyfile sends every path it doesn't serve there too, so links to the
 * console's old address (dock.nordtempo.com) keep working.
 */
export const site = {
  consoleUrl: "https://console.nordtempo.com",
  earlyAccessEmail: "dock@nordtempo.com",
  repoUrl: "https://github.com/NordTempo/tempoDock",
  installGuideUrl:
    "https://github.com/NordTempo/tempoDock/blob/main/docs/guide/install.md",
} as const;

/**
 * The one-line installs. The Caddyfile redirects both paths to the
 * latest release's scripts, which check what they download against the
 * release's signed SHA256SUMS.
 */
export const install = {
  server: "curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash",
  cli: "curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- https://dock.example.com",
  brew: "brew install nordtempo/tap/dock",
} as const;

/** A mailto link for early access requests, with the subject filled in. */
export const earlyAccessHref = `mailto:${site.earlyAccessEmail}?subject=${encodeURIComponent("Tempo Dock early access")}`;
