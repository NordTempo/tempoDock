# 0009: Dynamic domains and automatic TLS

Status: accepted; superseded in part by [0042](0042-domain-verification.md) (a hostname is unique per verified claim); amended by [0036](0036-cloudflare-api.md) (domains from Cloudflare)

## Context

Adding a domain should be one step: type a hostname in the CLI or UI,
point DNS at the server, done. Any hostname should work: apex domains,
subdomains, multiple domains per service, and platform-generated subdomains.
Nobody should ever edit proxy config by hand.

Constraints:

- Certificates must be issued automatically, including for domains whose DNS
  is set up after the domain is added.
- The server must not issue certificates for arbitrary hostnames someone points
  at it (abuse and Let's Encrypt rate limits).
- Domain changes must not drop traffic for other services.

## Decision

**The platform DB is the only source of truth for routing.** Domains are rows
in the `Domain` table, created through the API like everything else.

1. **Render, then load.** On any change (domain added or removed, release
   cutover), the daemon renders the complete Caddy config from the DB and
   loads it in one call. Caddy validates it and swaps atomically; open
   connections finish on the old config. If the new config is invalid, Caddy
   keeps the old one.
2. **Heal on start-up.** The daemon re-renders and reloads on boot, so any
   drift is corrected automatically.
3. **On-demand TLS, gated by the DB.** Caddy obtains a certificate the first time
   a hostname is requested, after asking the daemon (`ask` endpoint) whether that
   hostname belongs to a service. Unknown hostnames are refused. DNS can be set
   up before or after adding the domain.
4. **Generated domains.** If a base domain is configured (e.g. `*.apps.example.com`
   pointing at the server), every service can get a generated hostname with
   one click or command. These use the same on-demand flow, so no DNS-provider
   plugin is needed.
5. **Unknown hosts get a small built-in 404 page**, not an error from Caddy.
6. **Caddy's config endpoint listens on a Unix socket** readable only by the
   daemon, so app containers and other local processes can't change routing.

## Domain lifecycle

```
added → waiting_for_dns → issuing_certificate → active
                                 └────────────→ error (with reason)
```

The daemon checks DNS for the hostname and reports what record is needed
(A/AAAA to the server's IP, or a CNAME to the service's generated domain).
Status is shown live in the UI and CLI.

## Rules

- A hostname belongs to at most one service (unique in the DB).
- Adding and removing domains needs the `deploy` scope. Removing a domain
  from a production environment shows a diff first.
- Optional per domain: redirect to another domain (e.g. `www` → apex).

## Consequences

- Users never see Caddy. Domains are a first-class platform feature in both
  clients.
- Rendering the whole config each time is simple and idempotent. It's cheap at
  our scale (hundreds of domains); revisit only if it ever isn't.
- If the daemon is down, existing domains and certificates keep working, but
  certificates for brand-new domains wait until it's back (the `ask` check
  fails closed).
- Generated subdomains each get their own certificate. Let's Encrypt limits
  certificates per registered domain per week, which is fine for NordTempo.
  A hosted offering with many generated subdomains would switch to a wildcard
  certificate via DNS challenge.
- Out of scope for now: raw TCP/UDP proxying (e.g. exposing Postgres publicly).

## Amendment (2026-10-30): domains from Cloudflare

A domain added "from Cloudflare" ([0036](0036-cloudflare-api.md)), in a
zone a Cloudflare connection reaches, on a tunnel server, needs no DNS
work: Dock routes it through the server's tunnel and creates its proxied
CNAME, and deletes both with the domain. Dock never creates records
pointing at an IP, and never changes one it didn't make. Other domains
still show the record to create.
