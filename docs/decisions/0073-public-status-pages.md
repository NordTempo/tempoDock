# 0073: Public status pages

Status: accepted (asked by Saile, 2026-11-10; plan and public page mock
A picked by Saile, Postplan `mqr6y0crg60j`). Builds on
[0055](0055-uptime-checks.md) (uptime checks) and
[0037](0037-analytics.md) (public dashboards, whose serving it copies).
Phases 1 to 5 of 5 are in; [Later phases](#later-phases) says what is left.

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

## Phase 2: incidents

| Topic | Decision |
|---|---|
| Incident | A title, a state and updates, on one page. States: **draft** (never public), investigating, identified, monitoring, resolved; the incident's state is its last update's. Publishing is posting the first update. A resolved incident stays resolved: something new is a new incident. IDs are 10 random base32 characters, in public URLs and the CLI, so they count nothing |
| Impact | Per component: degraded, partial outage or major outage, replaced whole by an update that names components. While the incident is open its components read at least that (manual ones too); resolved, its impact stays recorded and colours the days it ran (amber for degraded, orange for partial, red for major) with its title in the day's hover text |
| Updates | Plain text up to 5,000 characters: blank lines make paragraphs, `https://` links become links, nothing else is markup. A posted update's words can be fixed: the page says "edited", the audit log keeps what it said |
| Drafts from alerts | When an `uptime_down` or crash loop alert opens about the sources of a published page's component (with `drafts` on, the default) and no open incident names it, Dock opens a draft: "<components> unavailable" (major) or "Errors on <components>" (partial), with a fixed sentence, never the alert's words. One draft per page per alert. The draft goes if its alert resolves before anyone publishes it; a published one stays. Dock never publishes |
| Public side | Open incidents with their updates lead the page; the last 14 days of resolved ones follow the components, a line per day; `/incidents/<id>`, `/history` (every published incident by month, 500 at most) and `/feed.atom` (the last 50 updates, one entry each; Atom only: every feed reader and Slack take it) are rendered and cached like the page. `status.json` gains the open incidents. Authors, drafts and alerts never reach them |
| Components keep their IDs | Writing a page's component list keeps a component given by ID, or else by name, so incidents keep naming it; others are removed with their impacts |
| Permissions | Reading incidents: read scope. Opening, updating, editing and publishing: deploy scope, since the people who deploy are on call; a token limited to a project or environment may name only components built entirely from what it sees, never none and never a manual one. Removing an incident: the page's admins. An actor that asks first (MCP `request_access`) can't post: an agent that shouldn't speak in public opens a draft instead, which needs no approval because nothing public changes. Locked environments don't apply: incidents change no environment. Audited as `incident.open`, `.draft` (by Dock), `.update`, `.publish`, `.resolve`, `.edit`, `.edit_update` and `.delete` |
| API | `POST /status-pages/{page}/incidents`; `GET`/`PATCH`/`DELETE /incidents/{id}`; `POST /incidents/{id}/updates`, `PATCH /incidents/{id}/updates/{update}`. A page's `GET` carries its incidents open or resolved in the last 90 days, drafts included |
| CLI | `dock incident list\|show\|open\|update\|publish\|resolve\|edit\|remove`, top level because on-call types it under stress; `dock status-page update --drafts` |
| MCP | `status_incident_open` (a draft unless the agent was asked to post) and `status_incident_update` (post, publish, resolve, or change a draft's words) |
| UI | The page's editor lists its incidents, each draft as a strip with "Review draft", and **New incident**. An incident's screen (mock E2): post an update with its state and impact, the timeline with who posted (and who drafted), and "Affected now": the named components' status and what they are built from, private |
| Migration | 0091: `status_incidents` (kinds and states for phase 3's maintenance too), `status_incident_components`, `status_updates`; `status_pages.drafts` |

## Phase 3: maintenance windows

| Topic | Decision |
|---|---|
| Window | An incident of kind `maintenance` (0091 made room): a title, a start, a planned end (at most 7 days later) and the components it takes down, public as soon as it is scheduled. States: scheduled, in progress, completed, cancelled. A window whose start has passed starts as it is scheduled |
| Start and end | A step on the minute statistics tick (`maintenanceTick`): with no window scheduled or in progress, nothing; otherwise one comparison with the next boundary kept in memory. At a boundary Dock moves the window and posts its own update ("Maintenance started.", "Maintenance completed."), audited as `maintenance.start` and `.complete` by Dock. Within a minute of the time, which is fine for maintenance. People start, complete or cancel sooner with an update, and move its times (`PATCH /incidents/{id}`: start while scheduled, end until it is over) |
| Alerts | While a window is in progress, alerts about the services its components show, and uptime alerts of its single checks, are recorded with `alerts.muted_by` and shown in Dock but sent to no channel, so they get no reminders either, and draft no incident. When the window is no longer in progress (completed, cancelled, removed, or ended while Dock was down) the alerts it held that are still open are sent as opened: a deploy that broke something during maintenance still pages someone. One map lookup per alert opening while a window runs |
| Uptime | The muted checks keep being probed, so Dock shows their state, but a probe writes no row and a failure doesn't count: planned downtime neither turns the component red nor dents its 90-day percentage |
| Status | A component a window in progress names reads **maintenance** (blue), over whatever else it would say; the page's headline is "Under maintenance: <components>" unless something is worse. The days a window ran have a blue mark unless something worse happened |
| Public side | Windows scheduled and in progress follow the open incidents at the top, soonest first, with their time ("Fri 13 Nov 02:00 to 02:30 UTC") and components; completed ones join the past incidents, cancelled ones only the history. `status.json` gains `maintenance` |
| API, CLI, MCP | `POST /status-pages/{page}/maintenance`; updates take the window's states. `dock maintenance schedule\|start\|complete\|extend\|cancel`; `dock incident list` shows windows with incidents. MCP `status_maintenance` (schedule, start, complete, extend, cancel). Deploy scope, with the incidents' rule for limited tokens. Unlike incidents, locked environments apply (added after the security review): a running window mutes its environments' alerts, their safety net, so a token that schedules, starts, moves or extends one over a locked environment's components gets `environment_locked` and an access request (ADR 0044); completing and cancelling don't need it |
| UI | **Schedule maintenance** on the page; a window's screen offers a note, starting, completing or cancelling it, and its times |
| Migration | 0092: `alerts.muted_by`, and an index of the windows not finished |

## Phase 4: custom hostnames

| Topic | Decision |
|---|---|
| Hostname | A page may also answer on one hostname of its org's (`hostname`, set with `PATCH /status-pages/{page}`), pointed at the page's own address with a CNAME. Organization admins |
| Proof | As for a service's custom domain ([0042](0042-domain-verification.md)): the TXT record `_dock-challenge.<hostname>` = `dock-verify=<token>`. Until it is verified it isn't routed and gets no certificate. No proof when Dock knows who controls the name: an operator sets it, it is under the base domain, or Dock makes its record in a Cloudflare zone the org may use |
| Checking | No checker of its own: Dock looks for the record when the hostname is set and whenever someone asks (`POST /status-pages/{page}/hostname/check`, `--check`, "Check now"). Service domains need the domain checker for their DNS and certificate states; a page's hostname has only the TXT to wait for, once |
| Who holds it | At most one page holds a hostname verified (a partial unique index); unverified claims don't block. A service can't add a page's verified hostname, nor a page a service's verified domain, Dock's own names, a dashboard's, another page's address or a Cloudflare redirect |
| Cloudflare | In a zone the org may use, when the control plane can take a tunnel, Dock makes a proxied CNAME to the control plane's tunnel like a domain's (`cloudflare_records.status_page_id`), and the tunnel reaches Caddy, which routes the hostname to `[status] listen`. Changing or removing the hostname, or the page, deletes the record |
| Serving | The verified hostname joins the page's address in the hosts Dock routes to the status listener, with on-demand certificates; the page, its incidents, history and feed answer on both |
| CLI | `dock status-page hostname PAGE [HOSTNAME] [--check] [--remove]`; `show` prints the records to add |
| UI | The page's settings: the hostname, its state, the records to add, Check now |
| Migration | 0093: `status_pages.hostname`, `verify_token`, `verified_at`; `cloudflare_records.status_page_id` |

## Phase 5: update hooks

| Topic | Decision |
|---|---|
| Hooks | A page's admins add Slack incoming webhooks, Discord webhooks and JSON webhooks signed like alert webhooks (HMAC-SHA256 in `Dock-Signature`, a secret generated and shown once unless given). The URL and secret are sealed (`sealedStatusHook`, bound to page and name); lists show only the URL's scheme and host |
| What is posted | Every public update of a published page: an incident opened or updated (not drafts), a window scheduled, started, completed or cancelled, whether by a person or by Dock. Chat hooks get a message (`Incident on Acme: API errors (Identified)`, what it affects, the words and the link; Discord without mentions, at most 2,000 characters); webhooks get `StatusHookPost` JSON. As public as the page: no authors, alerts or services. Edits of past updates aren't posted again |
| Delivery | Through the alert engine: its client (no redirects, never this server, link-local or metadata addresses), its per-target ordered queue (now `deliveryQueue`, shared by channels and hooks, at most 32 waiting each) and its retries (0, 10 s, 1, 5 and 15 min). The last success and failure show on the hook; **Test** posts once without retries |
| No subscribe form, no mail | Letting anyone register URLs would have Dock POST wherever a stranger says, and confirming them needs mail, which Dock doesn't send. Visitors follow the Atom feed; a partner sends the page's admins a webhook URL. The public listener stays GET only |
| API, CLI, UI | `GET`/`POST /status-pages/{page}/hooks`, `DELETE .../hooks/{hook}`, `POST .../hooks/{hook}/test`; `dock status-page hook list\|add\|remove\|test` (URLs from stdin with `-`); the page's **Update hooks** section. Audited as `status_page.hook_add` and `.hook_remove` |
| Migration | 0094: `status_hooks` |

## Performance

| What | Cost |
|---|---|
| No pages, or nobody viewing | nothing; an event costs one atomic add |
| A view of a rendered page | a map lookup, the client's bucket and one write (`BenchmarkStatusServe`, ~2 µs) |
| A render | seven queries and the template (`BenchmarkStatusPage100`, ~12 ms for 100 components, 90 days and 50 incidents, most of it the history query; `BenchmarkRenderStatusPage`, ~3 ms). At most one per page every 5 s while it is viewed and changing, one a minute otherwise |
| An incident page, history or feed | two or three indexed queries per render, cached like the page |
| An alert that may draft | one indexed query when an `uptime_down` or crash loop alert opens, one delete when it resolves |
| Hooks | one indexed query per public update; posting happens on the hook's own goroutine, which exits when its queue is empty |
| Maintenance | nothing without windows; a time comparison a minute while one is scheduled or running, a map lookup per alert and per probe while one runs; one query to reload the windows when one changes |
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
2. Incidents (above).
3. Maintenance windows (above).
4. Custom hostnames (above).
5. Update hooks (above).
6. Surviving the control plane, if wanted.

## Consequences

- A team can replace a hosted status page with one built from the checks
  Dock already runs, and the two can't disagree.
- One more loopback listener on the control plane.
- Project names `alerts`, `errors`, `help` and `status-pages` are now
  refused, like the organization's other pages; a test keeps the list in
  step with the UI's routes.
- A page's 90-day history starts with the 30 days Dock already has.
