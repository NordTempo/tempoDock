# 0059: An Analytics area, a slimmer service page, project settings and uptime checks that set themselves up

Status: accepted (Saile decided the layout and the moves; the details
below are the agent's)

## Context

A service's page had 15 tabs. Most visits to a service are deploying or
solving a problem (logs, errors, traffic); analytics is a different
question, about a site rather than a service, and it will grow tabs of its
own. Releases had a tab and a short list on the Overview. Settings mixed
how a service is built and run with archiving it. The project page carried
its name, settings and archive in its body and a rename button in its
header. And uptime checks (ADR 0055) only existed once someone added one.

## Decision summary

| Topic | Decision |
|---|---|
| Analytics area | An **Analytics** entry in the organization's sidebar, `/<org>/analytics`: the org's live sites the caller may read, grouped by project, with their visitors and page views over the last 24 hours (Saile's pick, mock C) |
| Sites | Organized by site as ADR 0037 has them, no data model change. A site is named within its project, so its page is `/<org>/analytics/<project>/<site>`, with a side menu: Overview (the dashboard), Vitals, Funnels and Replays (and a replay's page under Replays). The range and filters stay when switching view. Since 2026-11-13 (Saile) the menu also has Errors and, last, Settings (`…/settings`): the site's name, hostnames, goals, collection, replay, retention and sharing as settings rows, where a dialog was; goals are added and removed there |
| The list's numbers | `GET /api/v1/sites` (`ListOrgSites`): four SQLite queries however many sites, and one `Dashboard` request per site to the analytics store (totals for the range and the one before, and a series; no breakdowns), at most four at once, at most 200 sites. The range is 24h, 7d or 30d (hourly series, daily over 30d); the page shows the sites as rows by project or as tiles, the choice kept in the browser. A store that can't answer leaves the numbers out, not the list. `analytics.*` audit events refetch it like the project's sites |
| Errors | Browser errors move to the **service**, an **Errors** tab next to Logs and Traffic at `…/<service>/errors`, G E, showing the site's errors as before. Its name and address leave room for backend errors, 5xx spikes and alerts later |
| Counting | Turning counting on and off for a domain moves from the old tab to the service's Domains tab |
| Service tabs | Overview, Data, Backups, Logs, Errors, Traffic, Run, Jobs, Resources, Uptime, Domains, Variables, Build & run, Audit (Data, Backups, Errors, Traffic and Uptime only where they apply). Analytics and Releases are gone; Settings is **Build & run** at `…/build` and keeps G S |
| Old addresses | `…/analytics` and its sub-pages redirect to the service's site (the `?site=` one, else the first its hostnames count for), `…/analytics/errors` to the Errors tab, `…/releases` to the Overview's `#releases`, `…/settings` to `…/build`. G Y goes to the service's site through the first, G R to the history |
| Overview | One uptime line (up or down, 24-hour uptime, the last incident), one analytics line (visitors in 24 hours, a link to the site), and the release history: 10 newest, then the 20 the release query holds, then up to 100 on request, with rollback and diff. Live release events patch the same queries as before |
| Danger actions | Archive leaves Build & run for the header's ⋯ menu (admins), next to Stop |
| Project settings | `/<org>/<project>/settings` with tabs: **General** (name and rename, placement, the config file's export and apply, archive), **Variables** (an environment's shared variables and log drains, one environment at a time, `…/settings/variables/<environment>`) and **Previews**. The project page keeps what runs (environments, problems, activity); its header loses Rename and Variables V for Settings S |
| Environment page | `/<org>/<project>/<environment>` redirects to the environment's shared variables in the settings, and `?service=web` to that service's Variables tab. Projects have no description or default environment, and none is added |
| Reserved names | Projects can't be named `analytics` or `requests` (pages of an organization), environments not `settings` or `compare` (pages of a project), so no name hides a page. Existing names keep working where they don't collide |

## Uptime checks that set themselves up

When a domain that serves a service (not a redirect) becomes verified,
Dock adds a check of `https://<domain>/` with the default settings (every
minute, 10-second timeout, expects 200), marked `auto` (column
`uptime_checks.auto`, `UptimeCheck.auto`):

- At verification (`addDomain` for domains that need no proof,
  `proveDomain` for the TXT record), if the service has a live release
  and isn't stopped or archived; otherwise at its first live release
  (a cutover without a previous release), so a first deploy doesn't page
  anyone before anything answers.
- Not for a domain whose DNS names no public address (`httpkit.IsPublicAddr`,
  the same rule probes dial by), which a probe couldn't reach: a LAN or
  tailnet install would only get alerts.
- Not when the service already checks that host, and not past the
  service's or organization's caps (skipped, not refused).
- Removing the domain removes its checks (ADR 0055 already did). A user
  may remove an auto check; Dock doesn't add it again, since it only adds
  at verification and at the first release. Changing one means replacing
  it with an own check; there is no edit.
- **Check all domains** on the Uptime tab (`POST …/uptime-checks/domains`,
  `dock uptime domains`, admin) adds the missing ones for domains verified
  before this, without the DNS test: it is asked for.
- Uptime checks aren't part of `dock.toml` or `dock project export`, so
  `dock apply` can't see auto checks as drift or remove them.

The Uptime tab shows the external checks as **From outside** and Dock's
own ongoing health check of the container (ADR 0043) as **From inside**:
its state, what it probes and its restarts, from the service's status. No
new prober.

## Consequences

- A service page has at most 14 tabs, most services 11 or 12, and the
  history, uptime and visitors are on its first screen.
- Analytics has room for its own tabs without crowding services.
- Every verified public domain is watched from outside without anyone
  remembering to add a check, at one probe a minute per domain.
- One more DNS lookup per verified domain, once, when its check is added.
