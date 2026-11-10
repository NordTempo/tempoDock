# 0073: Public status pages

Status: accepted (asked by Saile, 2026-11-10; plan and public page mock
A picked by Saile, Postplan `mqr6y0crg60j`). Builds on
[0055](0055-uptime-checks.md) (uptime checks) and
[0037](0037-analytics.md) (public dashboards, whose serving it copies).
Phase 1 of 5; the later phases are under [Later phases](#later-phases).

## Context

Dock knows when production is down: uptime checks from another server
([0055](0055-uptime-checks.md)), crash loops and unhealthy services
([0020](0020-retention-and-alerts.md), [0043](0043-ongoing-health-checks.md)).
The people who use those apps learn none of it, so a team pays for
Statuspage or Instatus and runs a second set of monitors that disagree
with Dock's.

| Force | Means here |
|---|---|
| Idle means idle ([performance](../performance.md)) | No timer; a page nobody views costs nothing, however often its checks change |
| Anyone may ask | A view never reads the database; bounded per client, bounded renders, bounded cache |
| Nothing private leaks | Service, project, server names, check URLs and errors never reach the public side |
| History | `uptime_stats` keeps hour rows 30 days; a status page shows 90 |
| Works when the app is down | The page is served by the control plane, never by the app |

## Decision

| Topic | Decision |
|---|---|
| Scope | Pages belong to the **organization**, at most 5. A page for one project is a page built from that project's services: one concept. Each has at most 100 **components** with a public name (typed by a person, never defaulted from a service's), an optional description and a group |
| Sources | A component is built from **services** (all their uptime checks, including those Dock adds for domains, [0059](0059-analytics-area-and-slimmer-service-page.md), and their health) and single **uptime checks**, at most 10, or none (manual). Only the org's live services; **previews are refused** (random names, short lives). A source whose service is archived later adds nothing |
| Status | From memory, on every read: of a component's checks (each counted once), all down is a **major outage**, some a **partial outage**; a source service's open crash loop alert is a partial outage, unhealthy **degraded**. A check unknown after a restart is down only while its `uptime_down` alert is open, so restarting Dock never turns a page red. An environment asleep ([0070](0070-sleeping-environments.md)) is **idle**: neutral, never down, and its asleep time is no data. `auto` off: operational until incidents (phase 2) set it. The page's status is its worst component, idle ones aside |
| History | `uptime_stats` gains **day rows** (`res` 86400, UTC days, kept 90 days). The statistics tick's hourly uptime rollup also rolls each finished day's hour rows into a day row: no new timer. After a restart it carries on from the newest day row; it never rolls a day whose hour rows are partly trimmed. The first roll after the upgrade fills the 30 days hour rows hold; older days read "no data" (gray), never green. A page reads day rows, then hour rows for days not rolled yet, then minute rows for the hour not rolled yet, in one query through `uptime_stats`' key |
| Addresses | `status-<slug>.<base domain>` (the control plane's), once published. A slug is one page's in the whole install, so no org can take or shadow another's; it can't be a service's domain, Dock's own hostname or a public dashboard, and services and redirects can't take a page's address, published or not. Unpublished, the address answers 404. Custom hostnames come in phase 4 |
| Serving | The control plane's Caddy routes published pages' hostnames to `[status] listen` (default `127.0.0.1:7079`), a listener serving nothing else: `GET /` and `/status.json`, 404 for anything else, with the client's address and certificates on demand like Dock's address. No access protection ([0030](0030-access-protection.md)) applies: these are not service domains |
| Rendering | A page is rendered when it is asked for after something it shows may have changed, or once it is a minute old; the gzipped bytes are served until then, with an ETag. "May have changed" is a generation counted on the event bus: every uptime, alert and sleep event, and every change to a page. Within 5 s of a render a stale page is served as it was. At most 2 renders run at once, the cache holds at most 8 MB, and a render that fails keeps the last bytes |
| Limits | Each client address gets 60 requests and then 2 a second |
| The page | Saile's pick, mock A: one column, the overall status, then per group each component's status and a bar of its 90 days (dark green on a perfect day, amber below 100%, red below 99%, gray without probes) with its 90-day percentage; hovering a day says its date and percentage. Pages can hide percentages (`show_uptime`): the bars keep their colours and say only "downtime". System fonts, no script, no requests: the CSP allows the page's own style by its hash and nothing else. A few KB gzipped (under 4 KB for 100 alike components) |
| Never public | The public view is its own type (`apitypes.PublicStatusPage`) holding names people chose, statuses and percentages: nothing else can reach the page or `status.json` |
| Permissions | Members read pages and the editor (read scope); sources in projects a limited token can't see read "another project". Making, changing, publishing and removing pages and their components: organization admins acting for the whole org (`requireUnlimited(admin)`). Audited as `status_page.create`, `.update`, `.publish`, `.unpublish`, `.components` (with what was added and removed), `.delete` |
| API | `GET`/`POST /status-pages`, `GET`/`PATCH`/`DELETE /status-pages/{page}`, `PUT /status-pages/{page}/components` (the whole ordered list), `GET /status-sources` (the services and checks a component may show) |
| CLI | `dock status-page list\|show\|create\|update\|publish\|unpublish\|remove`, `dock status-page component add\|remove\|move` |
| MCP | `status_pages` (read): every page or one, with what each component says now. Changing pages stays with people |
| UI | Status pages in the organization's sidebar: the list, and a page's editor (mock E1): components with what each shows (private), status now, 90-day bar and uptime, reordering, a dialog to add or change one, and the page's settings |
| Migration | 0085: `status_pages`, `status_components`, `status_sources` |

## Performance

| What | Cost |
|---|---|
| No pages, or nobody viewing | nothing; an event costs one atomic add |
| A view of a rendered page | a map lookup, the client's bucket and one write (`BenchmarkStatusServe`, ~2 µs) |
| A render | four queries and the template (`BenchmarkStatusPage100`, ~10 ms for 100 components and 90 days, most of it the history query; `BenchmarkRenderStatusPage`, ~3 ms). At most one per page every 5 s while it is viewed and changing, one a minute otherwise |
| Day rollup | one statement per finished day and one prune, on the hourly rollup |

The history query reads one row per check and day, so a 10-component
page renders in about 1 ms. Budgeting the 100-component page at 25 ms
instead of folding days in SQL keeps the query plain.

## When the control plane is down

A status page is served by the control plane, as are the uptime checks'
schedules: when its server is down, so is the page, and Dock knows
nothing new to say anyway. A daemon restart is a second's gap (Caddy is
a separate process). Serving a last copy from elsewhere (Cloudflare's
cache with `stale-if-error`, which the page's `Cache-Control` already
allows, or a snapshot route on another server) is phase 6, decided
after real use.

## Later phases

Each its own pull request, numbered when it merges.

1. Pages, components, status, history, the public page (this).
2. Incidents: states (draft, investigating, identified, monitoring,
   resolved), updates with impact per component, a draft opened from an
   `uptime_down` or crash loop alert and dropped if the alert resolves
   first (never published by Dock), incident and history pages, an Atom
   feed; `dock incident`, MCP tools to open and update them (an
   ask-first agent writes drafts, a person publishes).
3. Maintenance windows on the minute tick: alerts of their services
   raised but held, sent at the end if still open; no uptime rows during
   the window.
4. Custom hostnames: TXT verification ([0042](0042-domain-verification.md)),
   certificates, a Cloudflare record when the zone is connected
   ([0036](0036-cloudflare-api.md)).
5. Update hooks: Slack, Discord and signed webhooks set by admins, through
   the alert delivery queue. No mail and no public subscribe form: anyone
   registering URLs would have Dock POST wherever a stranger says.
6. Surviving the control plane, if wanted.

## Consequences

- A team can replace a hosted status page with one built from the checks
  Dock already runs, and the two can't disagree.
- One more loopback listener on the control plane.
- Project names `alerts`, `errors`, `help` and `status-pages` are now
  refused, like the organization's other pages; a test keeps the list in
  step with the UI's routes.
- A page's 90-day history starts with the 30 days Dock already has.
