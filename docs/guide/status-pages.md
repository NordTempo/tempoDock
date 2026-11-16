# Status pages

A status page tells the people who use your apps how they are doing:
each part of your product, its status now and its last 90 days. Dock
builds it from the [uptime checks](../decisions/0055-uptime-checks.md)
it already runs and serves it at `status-<name>-<random>.<base domain>`
([ADR 0073](../decisions/0073-public-status-pages.md); the random part,
[ADR 0087](../decisions/0087-unguessable-generated-hostnames.md), keeps
anyone from finding it by guessing).

## Make one

```sh
dock status-page create acme --title "Acme"
dock status-page component add acme API --service shop/production/api --group Core
dock status-page component add acme "Web app" --service shop/production/web --group Core
dock status-page publish acme
```

Or in the UI: **Status pages** in the sidebar, then **New status page**.
Organization admins make and change pages; every member sees them.

- **The name** is unique in your organization; another organization may
  use the same. `dock status-page rename acme acme-api` (or Edit next to
  the name in the page's settings) moves the address to
  `status-acme-api-<random>.<base domain>`, the same random part; the
  old address redirects there, and the old name still finds the page,
  until another of your pages takes it. A custom hostname stays as it is.
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
services do; only incidents change it.

## Your own hostname

```sh
dock status-page hostname acme status.acme.com
```

It prints two records to add at your DNS provider: a TXT record that
proves you control the name, and a CNAME to the page's own address
(`status-acme-<random>.<base domain>`, shown by `dock status-page show`). Then `dock status-page hostname acme
--check` (or **Check now** in the page's settings) verifies it, and the
page answers on both names with its own certificate. If the name is in a
Cloudflare zone your organization has connected, Dock makes the record
itself and nothing needs proving.

## Following updates

Visitors follow the page's Atom feed (`/feed.atom`); Slack, Discord and
every feed reader take it. To post updates somewhere yourself:

```sh
dock status-page hook add acme team --slack - < slack-webhook-url
dock status-page hook add acme partners --webhook https://partner.example.com/dock-status
dock status-page hook test acme team
```

Every public update of an incident or maintenance window is posted, with
retries. A webhook gets signed JSON (`Dock-Signature`, like alert
webhooks; the secret is printed once). There is no public sign-up form
and no mail: a partner who wants updates sends you a webhook URL.

## Incidents

An incident is what you tell visitors about an outage, update by update.

```sh
dock incident open acme "API errors for some requests" --component API:partial -m "We are investigating."
dock incident update 4f2kqz7mna --state identified -m "A slow index rebuild; we are moving traffic off it."
dock incident resolve 4f2kqz7mna -m "Fixed. Errors stopped at 14:20 UTC."
```

Or on the page in Dock: **New incident**, then the incident's screen to
post updates. Everyone who can deploy opens and updates incidents;
admins remove them.

- **Posting is public at once.** The title and every update show at the
  top of the page while the incident is open, on its own page
  (`/incidents/<id>`), in `/history` and in the Atom feed (`/feed.atom`,
  which Slack, Discord and feed readers follow).
- **Impact** is per component: degraded, partial or major. While the
  incident is open, each component it names reads at least that,
  whatever its checks say, and the days it ran are coloured on the bars.
  Resolving it lets the checks speak again.
- **Updates are plain text.** Blank lines make paragraphs and `https://`
  links become links. Fix a typo with `dock incident edit ID --update
  UPDATE -m ...`; the page says the update was edited.
- **Drafts** stay in Dock: `--draft`, or **Save draft**. Publish one with
  `dock incident publish ID` or **Publish** on its screen.

When an uptime check goes down or a service crash loops, and a published
page has a component built from it with no open incident, Dock writes a
draft for you ("API unavailable", major outage) and shows it on the page
in Dock. It never publishes it: the component already reads down, and
the words are yours. If the alert resolves before anyone publishes the
draft, the draft goes. `dock status-page update acme --drafts=false`
turns this off.

## Maintenance

```sh
dock maintenance schedule acme "Database upgrade" --component Database --from 2026-11-13T02:00Z --for 30m -m "Writes pause for up to 5 minutes."
dock maintenance extend 4f2kqz7mna --by 15m
dock maintenance complete 4f2kqz7mna
```

Or **Schedule maintenance** on the page in Dock. A window is public as
soon as it is scheduled. Dock starts it at its start and completes it at
its end (within a minute); you can start, complete or cancel it sooner,
or move its times. While it runs:

- its components read **maintenance** (blue), whatever their checks say;
- alerts about their services are kept in Dock but not sent; those still
  open when the window ends are sent then, so something that broke during
  the work still reaches you;
- their uptime checks keep running but don't count, so planned downtime
  doesn't dent the 90-day percentage. The day gets a blue mark.

Times are UTC unless they carry an offset.

Agents can open and update incidents too (`status_incident_open`,
`status_incident_update`); asked to, they write drafts for a person to
publish.

## What anyone can see

Only what you typed (the page's title, components' names, groups and
descriptions, incidents and their updates), statuses and percentages.
Never service, project or server names, check URLs, errors, who posted
an update or which alert drafted it. Besides the page, `/status.json`
has the same in JSON (open incidents included) for other dashboards and
scripts.

## Limits

- The page is served by Dock's control plane: when its server is down, so
  is the page.
- History starts with the 30 days of uptime results Dock kept before
  status pages; older days show gray.
- At most 5 pages per organization, 100 components per page.
