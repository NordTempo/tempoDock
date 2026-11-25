# 0099: One obvious place for each feature

Status: accepted (Saile picked the v4 before and after mocks on
2026-11-24, Postplan `8k22xveyb52d`; the Projects page, the environment
strip and the failed deploy on the Overview came from the Pipeline mock
on 2026-11-25, Postplan `knc9mvckx1v6`). Amends
[0076](0076-service-page-in-nine-tabs.md) (settings views, tab order,
the save model's place stays) and [0062](0062-org-alerts-and-operators.md)
(the settings column's groups).

## Context

Dock does a lot, and every screen showed most of it at once. Saile found
the UI overwhelming and, worse, hard to find things in. Going through the
app turned up the reasons:

- **One feature, several homes.** Variables could be edited from four
  places, and a service's Variables tab was the whole project's editor.
  Domains and backups each had a Manage button on the overview and a
  view under Settings. Uptime checks lived in Metrics while the health
  check lived in Settings.
- **Menus that didn't match their name.** A service's More held one
  item (Archive); an environment's More mixed a server name, two
  settings pages, a create, a clone and a move.
- **Each level's settings looked different**: a column of sections for
  the org, tabs for a project, views under a tab for a service.
- **Tasks under nouns**: Run a command inside Jobs, placement inside a
  project's General, the Platform console inside the org switcher.
- **Search didn't find features**: "domain", "backup" or "health check"
  found help topics only.

The top bar for the organization and the sidebar for projects stay:
Saile preferred them to one combined sidebar (round 1 of the mocks).
The sidebar shows on the Projects page only (Saile, after the merge):
inside a project the page has the full width, and the top bar's
Projects leads back to the list.

## Decision

Placement rules, applied to every page:

1. **Every level has the same shape.** Org, project, service and server
   pages: a header with the name, its state, one primary button (plus at
   most one everyday action beside it, Restart on a service) and a `···`
   menu; then tabs; Settings is always the last tab.
2. **To change something, open its Settings.** Every Settings page is
   one page with its sections listed on the left (`ui/sections.tsx`),
   named after the job: Source, Running, Deploys, Health and uptime,
   Domains, Backups, Archive for a service; General, Variables,
   Previews, Placement, Log drains, Config file, Archive for a project.
   The old view addresses redirect to the section's anchor.
3. **One home per setting**, at the level it applies to. Other pages
   show it and link there. A service's Variables tab shows what the
   service sees, grouped by where each is set; the project-wide editor
   is the project's Settings.
4. **`···` holds every other action** in one order (operate, run,
   connect, then archive in red), never a menu with one item.
5. **A problem is said once with the button that fixes it**; elsewhere
   it's a mark or a count that links there. The organization's home,
   Projects (first in the top bar), lists the first five open alerts
   with their buttons, as the Inbox does, and the rest as a count into
   the Inbox. A failed deploy is said on the service's Overview with its
   cause and fix, not only on the release's page.
6. **Show what was set, fold the defaults.** The action a list exists
   for (Restore, Show) stays visible on every row; rare and destructive
   ones go in the row's `···`.
7. **Search knows every settings section and the actions people look
   for**, by the names the pages use.
8. **A project reads as a pipeline.** Its environments in the order a
   release goes through them (previews, then the others, production
   last) and, for each service, what lies between two: the same image,
   settings that differ, a newer image with Promote, or an image that
   failed there. Projects shows it as a grid for each project with
   something going on (quiet ones fold to a row); a service's Overview
   shows its own row as a strip, and a failed deploy names the
   environment where the same image works and the settings that differ
   (`features/overview/pipeline.ts`).

Kept from 0076: key hints on buttons, Apply in the service header while
changes wait, the review drawer.

## Consequences

- Finding a feature has one answer per kind of thing: org-wide in the top
  bar, a project's or service's in its tabs, any setting under Settings.
- Fewer buttons on every page; actions people used to see as buttons
  (Stop, Roll back) are one click further, in `···`, with their keys.
- Pages with a section list are longer than one view was; the list and
  the anchors keep them navigable, and search jumps straight to a
  section.
- Blind reviews earlier scored tab-only changes lower (0076's trials).
  This changes placement, not just tabs, and should be measured with
  "find X" tasks on the review kit.
