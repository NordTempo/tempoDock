/** Addresses the pages link to. */
export const site = {
  earlyAccessEmail: "dock@nordtempo.com",
  repoUrl: "https://github.com/NordTempo/tempoDock",
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
