# 0109: Read-only security checks of each server's host

Status: accepted (decided by the agent; Saile set the scope and the rule
that results are the platform's, 2026-11-27). Builds on
[0025](0025-platform-console.md), [0062](0062-org-alerts-and-operators.md)
and [0097](0097-outside-heartbeat-and-calmer-alerts.md).

## Context

Dock runs on servers its operators set up by hand or from a provider's
image. The host under Dock is where most real compromises start: SSH
that takes passwords, a database someone published on every address
while testing, Docker's API on TCP, a dashboard container holding the
Docker socket, a kernel fix waiting for a reboot. Dock knows none of it,
and the operator only finds out by looking.

Dock's own app firewall (`internal/node/firewall`) already has an alert
when it isn't installed. Nothing covers the rest.

## Decision

### The checks

Each server checks its own host, as the unprivileged `dock` user the
daemon runs as, by reading only: config files, `/proc`, one list of the
host's running containers, and a simulated apt upgrade over the package
lists the host's own apt timer keeps fresh. Nothing changes the host,
nothing uses the network, nothing needs root. A check runs in about a
second (the apt simulation is most of it).

| Check | Finding | Severity |
|---|---|---|
| `ssh_password` | sshd takes passwords: `PasswordAuthentication`, or keyboard-interactive through PAM | warning |
| `ssh_root` | `PermitRootLogin yes`: with passwords on, critical; with keys only, info | critical / info |
| `open_port` | a TCP port listening on a public address (every address, or a global one that isn't private or Tailscale's, IPv4 or IPv6) that neither Dock nor sshd opened, one finding per port; a port another container publishes (matched by address and port) names it. A port on a private address only is info: providers like EC2 map a public address to it | warning / info |
| `docker_tcp` | Docker's API on TCP (`daemon.json` hosts, dockerd's `-H`, or a socket on 2375/2376 off loopback): without TLS critical, with client certificates warning | critical / warning |
| `docker_socket` | a running container mounts the Docker socket (or `/run`, `/var/run`, `/`), one finding per container | warning |
| `security_updates` | `apt-get --simulate dist-upgrade` would install packages from a `-security` archive; says when the lists are over a week old | warning |
| `reboot_required` | `/run/reboot-required` is there; warning once it is a week old | info / warning |
| `auto_updates` | unattended-upgrades missing, or off as `apt-config` reads it | info |
| `host_firewall` | neither ufw, firewalld nor an nftables ruleset that drops anything is on | info |
| `app_firewall` | Dock's app firewall isn't installed (only where one is expected, under systemd) | warning |

Every finding says what to do next (a command or a line to put in a
file). A check that can't read what it needs (a `sshd_config.d` file
readable by root only) says so as **unknown**, with the command an
operator runs to see; a check that doesn't apply (no sshd, not an apt
system) is **skipped**.

What was left out, and why:

- **The firewall's actual rules.** Listing them needs `CAP_NET_ADMIN`.
  The host firewall check reads what an unprivileged user can (ufw's
  config, enabled units, the nftables file), and is only info: a
  provider's firewall in front of the server doesn't show from the host
  at all, which is why open ports are their own finding.
- **UDP ports.** Fewer services, more noise (mDNS, DHCP, Tailscale);
  TCP covers the usual mistakes.
- **dnf and other package managers.** Dock's install targets Debian and
  Ubuntu; other systems skip the update checks rather than guess.
- **`sshd -T`.** It needs root. The checks read the files in sshd's
  order instead: first value wins, `Include` in place, trailing comments
  dropped, and `Match all` overriding the global section as it does in
  sshd. Match blocks with criteria (a user, an address) are left out:
  the checks report what anyone connecting gets, not what a matched
  user or address does. A file or include directory the daemon can't
  read makes the check unknown rather than a pass.
- **Naming the process behind a port.** Mapping a socket to its process
  needs root; the finding gives the `ss` command that does.

The checks see every running container's name, mounts and published
ports: a read of the host, which the runtime rule about touching only
Dock's resources allows. Nothing is done to a container that isn't
Dock's.

### Where results go: the platform only

The host is the platform's business (ADR 0062, "Servers are the
operator's"). Results live only in the platform console and its API:

- The console's **Security** tab beside Servers (Saile picked mock B of
  three): open findings across every server, worst first, with what to
  do; what couldn't be told; the dismissed ones with their reasons and
  Restore; each server's last check with Check now, and Check all now.
  The `host_security` alert links there.
- `GET /api/v1/host-checks` (every server) and
  `GET /api/v1/servers/{server}/host-checks` for operators and platform
  viewers (`platform.view`); Check now (`POST` on the same path),
  dismiss and restore for operators (`servers.manage`).
- `dock server checks <name> [--now]`, `dock server checks dismiss
  <server> <finding> --reason ...`, `dock server checks restore`.
- MCP `host_checks`, listed only to platform credentials and callers
  with a platform role, like `list_servers`.
- Never on org pages, in org alerts, an org's Needs attention or Inbox,
  or to org members and their agents.

### Cadence: about daily, on the statistics tick

No new timer. The statistics tick starts a server's check when its last
one is a day old (a map lookup and a time comparison per online server);
the check runs in the background and its results are one row per
server. When each server last checked is read once on start, so a
restart doesn't check every server again. A check that failed (an
offline server, an older node) waits a day like one that worked; Check
now runs one at once, or waits for the one under way.

The control plane asks remote servers over the node link: `check-host`,
a new op. Older nodes answer with "upgrade dock on it", shown as the
check's error.

### Alerts: one per server, calm

New warning and critical findings open one platform alert per server,
`host_security` ("3 security findings on server vps-2"), at the worst
finding's severity. Its target lists every finding raised while it is
open: a new finding counts and is sent as a repeat (ADR 0097), one fixed
only changes the text, and the alert resolves when none is open, fixed
or dismissed. Info findings never alert. `app_firewall` doesn't either:
it has its own alert. A daily check that finds the same things sends
nothing.

### Dismissing

An operator dismisses a finding with a reason ("Hetzner's firewall
allows 22, 80 and 443 only"). It shows as dismissed, with the reason and
who, stops counting and alerting, and stays dismissed if it goes away
and comes back, until someone restores it. Dismissals are per server and
per finding (`open_port:5432`, `host_firewall`); there is no expiry,
since a host's setup changes on purpose, not with time.

## Consequences

- Migration 0139: tables `host_checks` (one row per server: when, the
  results as JSON, the last error) and `host_check_dismissals`. Both go
  with their server.
- Node protocol 38: `check-host`.
- A new alert kind, `host_security`, and `AlertTarget.findings`.
- A host where the daemon can't read a file the checks need says so
  instead of guessing; an operator who wants the full answer runs the
  command the result names.
- Not done: other package managers, UDP, a fix button (every fix needs
  root, which the daemon doesn't have and shouldn't), dismissing one
  finding on every server at once.
