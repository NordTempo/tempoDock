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
| Tabs | Overview, Logs, Requests, Errors, Metrics, Jobs, Data, Variables, Settings, History. A web service shows 9, a worker 6, a database 8 |
| Requests | `…/<service>/requests`, G T: views under one tab, each its own route. Traffic (the index, ADR 0033), Visitors (the site's dashboard, ADR 0037, in the service's shell; Vitals, Funnels and Replays stay in the Analytics area) and Traces (ADR 0075, built by tracing into this tab). Only for services with a domain |
| Merged pages | Metrics is Resources and Uptime; Jobs holds scheduled jobs and one-off commands; Data gains Backups for databases; Settings is Build & run, Domains and (for stateful services that aren't databases) Backups on one page with an index; History is releases and the audit trail, and a release opens in it |
| Old addresses | Every old tab redirects to its new place with its search: `…/traffic` to Requests, `…/analytics` to Requests' Visitors, and the others as their screens move |
| Save model | Every setting is a row with Edit: Enter saves, Esc cancels. A setting that changes a running service is staged until Apply; every other saves at once. Checkboxes save at once only outside services |
| Staged changes | One control in the service header (the count and Apply, Ctrl Enter), a review drawer with the diff, the downtime warning and Discard. Nothing pushes the page |
| Failures | Said once, on the service's Overview, in a "Needs attention" list; elsewhere a dot, chip or count. In the organization, Inbox is the one list: Alerts and access requests together |
| Organization sidebar | Inbox, Errors, Analytics, Images, Status pages, Activity, Settings (Members and alert channels inside), then Help and the account |

## Consequences

- Requests is a service's word for HTTP requests; access requests are
  approvals in the Inbox.
- Keys: G T Requests, G Y its Visitors view. The other G keys follow the
  merged tabs as their screens move.
- `web/src/features/service/tabs.ts` stays the one list the strip, the
  palette and the ? help read.
