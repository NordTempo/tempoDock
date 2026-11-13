# 0076: The service page in nine tabs, one save model, a failure said once

Status: accepted (the coordinator picked option C of the navigation mocks,
Postplan `ehelv1jcks2r`, on Saile's behalf; the details are the agent's).
Built screen by screen; this ADR says where each step stands.

## Context

Two days of features after the redesign (#156) left the service page at up
to 14 tabs, with pages under a service that leave its shell (its Analytics,
a release), a three-row strip of staged changes pushing every tab down,
four ways to save a setting, and one failed deploy said in six places.
Tracing (ADR 0075) needs a home for traces next to what a service served.
Blind reviews of the old strip with fewer tabs, groups or a side list did
not move the scores, so this changes what the pages are, not how the strip
looks.

## Decision

| Topic | Decision |
|---|---|
| Tabs | Overview, Logs, Requests, Errors, Metrics, Jobs, Data, Variables, Settings, History, in that order. A web service shows 9, a worker 7, a database 8 |
| Requests | `…/<service>/requests`, G T: views under one tab, each its own route. Traffic (the index, ADR 0033), Visitors (the site's dashboard, ADR 0037, in the service's shell; Vitals, Funnels and Replays stay in the Analytics area) and Traces (ADR 0075, built by tracing into this tab). Only for services with a domain |
| Merged pages | Metrics is Resources, then Uptime (`#uptime`) where a service has a domain or checks; Jobs is Run a command (`#run`), then the scheduled jobs; Settings has views like Requests, each its own route: Build & run (the index), Domains (`…/settings/domains`) and, for every stateful service, Backups (`…/settings/backups`, its restore points). A first build as one long page with an index hid Domains and Backups below the fold, and blind reviewers failed to find them; History is the releases, then the activity (`#activity`), and a release opens under it (`…/history/<n>`) inside the shell. The Overview keeps the newest 5 releases and shows Domains (manage) and, for stateful services, Backups facts linking to those views. Backups sit with volumes in Settings for databases too: one rule, and the Data panel (shared with tempoBase) stays as it is |
| Old addresses | Every old tab redirects to its new place with its search or anchor: `…/traffic` and `…/analytics` to Requests, `…/resources` and `…/uptime` to Metrics, `…/run` to Jobs, `…/build`, `…/domains` and `…/backups` to Settings and its views, `…/releases`, `…/releases/<n>` and `…/audit` to History |
| Save model | Every setting is a row with Edit: Enter saves, Esc cancels. A setting that changes a running service is staged until Apply; every other saves at once. Checkboxes save at once only outside services |
| Staged changes | One control in the service header (the count and Apply, Ctrl Enter), a review drawer with the diff, the downtime warning and Discard. Nothing pushes the page |
| Failures | Said once, on the service's Overview, in a "Needs attention" list; elsewhere a dot, chip or count. In the organization, Inbox is the one list: Alerts and access requests together |
| Organization navigation | Saile's pick (option 3 of the navigation mocks, 2026-11-11). A top bar: the organization switcher (with the Platform console for operators), the organization's views (Overview, Errors, Analytics, Images, Status pages), Search (Cmd K), Inbox with its count, Organization settings (Members, Activity, alert channels inside; named in full because the project page has Project settings), Help (`?`) and the account menu (account, Help, Log out). On the left the projects: a rail of tiles (initials and a health dot) that expands into the full tree (environments, services, status, release). Expanded stays expanded until collapsed again (per browser; `[` toggles); nothing opens on hover. The organization's home is its Overview: what needs someone, the projects with their health, the most frequent errors, recent deploys and the status pages. On narrow windows the views fold into one menu and the projects behind a Navigation button. A first version as one sidebar of ten rows (and then seven) was replaced by this |

## Consequences

- Requests is a service's word for HTTP requests; access requests are
  approvals in the Inbox.
- Keys: G O, G L, G T (Requests; G Y its Visitors), G E, G M, G J, G B
  (Data), G V, G S (and S), G H. G U, G C, G K, G D, G A and G R are gone
  with their tabs.
- `web/src/features/service/tabs.ts` stays the one list the strip, the
  palette and the ? help read.
- Help (2026-11-13, Saile's pick B of the help mocks): `?` opens a panel
  beside the page with the topic for it instead of one long Help page,
  and takes in the keyboard shortcuts window. See docs/design/README.md.
