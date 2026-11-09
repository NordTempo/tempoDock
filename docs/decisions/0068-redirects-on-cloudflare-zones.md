# 0068: Redirects on Cloudflare zones

Status: accepted (Saile's decisions and mock pick, 2026-11-09).
Builds on [0036](0036-cloudflare-api.md) and [0062](0062-org-alerts-and-operators.md).

## Context

Saile wants hostnames that only send people elsewhere, like
`instagram.nordtempo.com` to `https://instagram.com/nordtempo`, set up
where the domain's zone is connected. A service domain's `--redirect-to`
(hostname to hostname, in Caddy) needs a service and a server, which a
link like this has nothing to do with.

## Decision

- **Cloudflare redirects at its edge.** For each redirect Dock makes two
  things in the zone, both marked as its own: a proxied placeholder record
  (`AAAA 100::`, the discard prefix; Cloudflare answers before any origin,
  so no request reaches it) and a Single Redirect rule in the zone's
  `http_request_dynamic_redirect` entry point ruleset. No server, Caddy or
  node protocol is involved, and a redirect works with every server down.
  The placeholder is the one IP record Dock writes; nothing is ever served
  from it.
- **Dock's rules only.** The ruleset holds rules made in the dashboard
  too. Dock adds, changes and deletes its own rules one at a time with the
  per-rule endpoints, never writing the ruleset back whole. Its rules carry
  the description `dock:<install ID> redirect <hostname>`, its record the
  usual `dock:<install ID>` comment. A record Dock didn't make at the name
  refuses the redirect, as for domains.
- **On the level its zone is added.** A redirect belongs to a connection
  and so to its owner: the platform's (operators, the console) or an
  org's own (its unlimited admins, Organization settings). A platform zone
  shared with orgs keeps its redirects on the platform; orgs neither see
  nor add them. With the zone on several of a level's connections, the
  oldest is used unless one is named.
- **What a redirect is.** A hostname (unique install-wide, and never also
  a domain), a target URL (`http(s)`, no quotes, backslashes or spaces,
  at most 2048 bytes), a status code chosen per redirect (302 by default;
  301, 307, 308) and whether the request's path and query are kept
  (`concat("<target>", http.request.uri.path)` with the query preserved).
- **Worker.** `cloudflare_redirects` (migration 0080) rows are worked by
  the Cloudflare worker like domains' records: pending, retried with
  backoff, a refused token waits for a new one, a missing permission
  fails naming `Zone › Single Redirect › Edit`. A token that loses the
  zone fails its redirects; they don't move to another connection. Live
  `cloudflare_redirect` events go to operators (the platform's) or the
  org's admins.
- **Removing a connection with redirects is refused,** naming them, unless
  forced (`dock cloudflare remove NAME --force`, the console's Force
  remove with the name typed): Dock deletes them in Cloudflare first. A
  token Cloudflare refuses, or one that lost the zone, leaves their rules
  behind, and the audit entry says which.
- **Surfaces.** `/cloudflare/redirects` (`?platform=true` for the
  platform's), `dock cloudflare redirects [add|set|remove|retry]`, and a
  Redirects tab on the Cloudflare page (mock C: a list with a filter and
  a zone pick, an Add redirect dialog).

## Consequences

- Tokens need one more permission for redirects; connections without it
  keep working for everything else.
- Cloudflare caps redirect rules per zone by plan (10 on Free). Past the
  cap, the redirect fails with Cloudflare's message.
- Only zones on Cloudflare. Redirects for other domains would need Caddy
  routes on a server; not now. Neither are path rules
  (`nordtempo.com/ig`), wildcards or bulk lists.
