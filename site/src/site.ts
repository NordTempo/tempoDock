/** Addresses the pages link to. */
export const site = {
  earlyAccessEmail: "dock@nordtempo.com",
  repoUrl: "https://github.com/NordTempo/tempoDock",
  /** The public demo (ADR 0100): the real UI on a fake runtime, reset hourly. */
  demoUrl: "https://demo.nordtempo.com",
} as const;

/**
 * The one-line installs. Both paths serve a small script (public/install.sh,
 * public/cli.sh) that runs the latest release's install script once it
 * matches the release's signed SHA256SUMS; that script checks what it
 * downloads the same way.
 */
export const install = {
  server: "curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash",
  cli: "curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- https://dock.example.com",
  brew: "brew install nordtempo/tap/dock",
} as const;

/** A mailto link for early access requests, with the subject filled in. */
export const earlyAccessHref = `mailto:${site.earlyAccessEmail}?subject=${encodeURIComponent("Tempo Dock early access")}`;
