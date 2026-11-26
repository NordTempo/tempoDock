# 0104: Analytics belong to the service

Status: accepted (Saile and the agent, 2026-11-26). Amends
[0037](0037-analytics.md): its "Sites", "Turning it on" and "Moves" rows.

## Context

Under 0037 a site is a set of hostnames, and history follows a hostname:
a domain removed and added again, on any service, carries its old site
with it unless `domain add --analytics new` says otherwise. That choice
can only be made while adding the domain, so the add-domain dialog asked
every user "Keep this hostname's earlier analytics, if it has any",
although it matters only for a hostname that was counted before.

Following the hostname was also never strict. A site came into another
environment only if the hostname was its only one and the actor could
read it there, since a hostname moving into another project would
otherwise show that project's people someone else's visitors.

Everything else Dock shows about traffic (Logs, Requests, Errors,
Metrics) belongs to the service. Domains come and go more often than
services do: generated hostnames, redirects, a switch to a Cloudflare
tunnel.

## Decision

| Topic | Decision |
|---|---|
| Sites | A site is one service's analytics: `sites.service_id`, at most one live site per service. The site lives in the service's environment, so staging and production still never mix. A site no longer spans services; `analytics enable --site` goes |
| Counted hostnames | Every domain of a counted service is counted, except redirects. A domain added later is counted from its first request; nothing is asked. `site_hostnames` goes: routing reads the service's domains |
| Turning it on | Per service: `dock analytics enable -s <service> -e <env>` (and `disable`), or the Analytics section of the service's Settings. Turning it off archives the site; turning it on again brings the service's last site back with its history. The first site starts the store, as before |
| Naming | A new site is named after the service's first custom domain that doesn't redirect (verified ones first), else the service; `-2`, `-3` when the project has a live site of that name. The name is a label: sites no longer rename themselves when a hostname goes away |
| Domains moving | Adding, removing or moving a domain never touches a site. Its past visits stay in the site of the service that served them; its new ones count for the service it is on now, if that service is counted. `domain add --analytics` and its API field go |
| Moving history | Explicit and later: `dock analytics move <site> --to <service>`, needing deploy on both environments, in the same org. The site is handed over whole (one row): its settings, goals, funnels and public address come along, and its events keep their `service` column, so the Service breakdown still shows who served them. The service it leaves stops counting. A target that already counts is refused: stop counting there first (its site is archived, its history kept). Merging two sites' events is left out until someone needs it |
| Markers | `added` and `removed` are no longer written for domains; `moved` marks history handed over from another service |
| Existing sites | The migration gives each site the service with the most of its hostnames (the earliest added on a tie); a service that led two live sites keeps the older and the other is archived. Another service whose hostnames a site counted gets a site of its own, named after its first such hostname, counting from the upgrade; its earlier visits stay in the first site, where the Service breakdown and filter tell them apart. A live site with no service left is archived |

## Consequences

- The add-domain dialog asks nothing about analytics. The Domains table
  loses its per-row Count visits column; the service's Settings get an
  Analytics section (counting, the script, moving history).
- A site that combined a marketing service and an app service becomes
  two sites. That was rare, and the org's Analytics area lists both.
- Replacing a service (`web` with `web-v2`) is one explicit move,
  instead of history silently following a hostname. The move hands over
  a whole site, so it needs no change in the analytics store and is
  instant; splitting one hostname out of a site could only have been
  exact within raw retention.
- One service's domains are told apart inside its site: the Domains
  breakdown and a `hostname` filter.
- History never crosses a project or environment without someone with
  access to both asking for it.
