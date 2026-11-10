# Status pages

A status page tells the people who use your apps how they are doing:
each part of your product, its status now and its last 90 days. Dock
builds it from the [uptime checks](../decisions/0055-uptime-checks.md)
it already runs and serves it at `status-<name>.<base domain>`
([ADR 0073](../decisions/0073-public-status-pages.md)).

## Make one

```sh
dock status-page create acme --title "Acme"
dock status-page component add acme API --service shop/production/api --group Core
dock status-page component add acme "Web app" --service shop/production/web --group Core
dock status-page publish acme
```

Or in the UI: **Status pages** in the sidebar, then **New status page**.
Organization admins make and change pages; every member sees them.

- **The name** is the page's address and one page's in the whole install.
- **Components** have a public name: what everyone sees. Pick names that
  don't name your services or servers ("API", not "postgres-primary").
- **What a component shows** is private: services (all their uptime
  checks and their health) or single uptime checks (`dock uptime list`
  shows their IDs). Preview environments can't be on a status page.
- A page starts unpublished; its address answers 404 until you publish it.

## What it says

| A component is | When |
|---|---|
| Operational | its checks are up |
| Degraded | a service it shows is unhealthy |
| Partial outage | some of its checks are down, or a service it shows is crash looping |
| Major outage | all its checks are down |
| Idle | its environment is asleep: not counted as downtime |

The page's headline is its worst component. The bars show 90 UTC days:
dark green for a perfect day, amber below 100%, red below 99%, gray
without probes. `dock status-page update acme --show-uptime=false` hides
the percentages; the colours stay.

A component added with `--manual` reads operational whatever its
services do.

## What anyone can see

Only what you typed (the page's title, components' names, groups and
descriptions), statuses and percentages. Never service, project or server
names, check URLs or errors. Besides the page, `/status.json` has the
same in JSON for other dashboards and scripts.

## Limits

- The page is served by Dock's control plane: when its server is down, so
  is the page.
- History starts with the 30 days of uptime results Dock kept before
  status pages; older days show gray.
- At most 5 pages per organization, 100 components per page.
