# 0004: Caddy as a separate proxy process

Status: accepted. Amended by
[0091](0091-switches-without-reloads.md): a route reads its upstream from
a file, so a traffic switch reloads no config.

## Context

We need a reverse proxy with automatic HTTPS and a way to switch traffic to a
new container atomically. Upgrading or restarting the daemon must never drop
traffic.

Candidates:

- **Caddy:** mature, automatic HTTPS, JSON admin API for live config changes,
  persists its last config (`--resume`). Flexible: wildcards, on-demand TLS,
  static sites, redirects.
- **kamal-proxy:** small Go proxy built for zero-downtime container cutover,
  with health checks and automatic TLS. Less flexible, but does part of our
  deploy logic for us.
- **Traefik:** label-driven, heavier, and its dynamic model fights explicit
  cutover control.
- **Embedding Caddy as a library:** one process fewer, but a daemon restart
  becomes a proxy restart.

## Decision

Caddy, as its own systemd service. Only the daemon configures it, through
Caddy's local config endpoint. Users never interact with Caddy: domains are
added through the platform's CLI or UI and the daemon turns them into Caddy
config (see [0009](0009-dynamic-domains-and-tls.md)). The daemon also owns the
cutover logic (health check, then route switch).

## Consequences

- Daemon restarts and upgrades never affect routing.
- We write and test health-gated cutover ourselves (it's also needed for
  rollback, so we'd want it anyway).
- Caddy's on-demand TLS is what makes "add any domain, any time" work safely,
  which tips the choice over kamal-proxy.
