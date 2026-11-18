/**
 * The one line that installs the dock CLI on a laptop and signs it in to
 * the Dock at `origin`: install-cli.sh (through the site's script, which checks it first)
 * installs the version that server runs, then runs `dock login`.
 */
export function cliInstallLine(origin: string) {
  return `curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- ${origin}`;
}
