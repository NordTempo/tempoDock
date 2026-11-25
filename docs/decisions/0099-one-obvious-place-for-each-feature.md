# 0099: One obvious place for each feature

Status: accepted (Saile picked the v4 before and after mocks on
2026-11-24, Postplan `8k22xveyb52d`; the Projects page, the environment
strip and the failed deploy on the Overview came from the Pipeline mock
on 2026-11-25, Postplan `knc9mvckx1v6`; the strip gave way to the
project header's environment buttons and a promote line on 2026-11-25,
Postplan `gtvj0aheh613`, mock D). Amends
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
The sidebar shows in Projects only (Saile, after the merge): on the
Projects page and every page inside a project, where Projects stays
marked in the top bar. The organization's other views (Errors,
Analytics, Images, Status pages, Inbox, Settings) have the full width.

## Decision

Placement rules, applied to every page:

1. **Every level has the same shape.** Org, project, service and server
   pages: a header with the name, its state, one primary button (plus at
   most one everyday action beside it, Restart on a service) and a `···`
   menu; then tabs; Settings is always the last tab.
2. **To change something, open its Settings.** Every Settings page is
   one page with its sections listed on the left (`ui/sections.tsx`),
   named after the job: Source, Running, Deploys, Health and uptime,
   Domains, Backups, Archive for a service. A project's are short
   enough for one row per setting (`SettingRow`) and split by scope
   (Saile, after the merge): its Settings tab holds the environment
   picked in the header and nothing else (Shared variables, Server,
   Log drains); a Project settings tab at the end of the row holds what
   applies to every environment (Name, Previews, New environments,
   Config file, Archive), with the environment switch greyed out
   while it's open. The old view addresses redirect to the section's
   anchor, on whichever page now holds it.
3. **One home per setting**, at the level it applies to. Other pages
   show it and link there. A service's Variables tab shows what the
   service sees, grouped by where each is set; the editor of an
   environment's shared ones is the project's Settings tab.
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
   says under its status only what asks for action (a newer image to
   promote, this image failing further on), and a failed deploy names the
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

## Amended 2026-11-25: a service's Settings in one column

Saile found the service's Settings page hard to read: the list of
sections on the left only repeated the headings beside it, rows behaved
five ways (chevrons, a bare checkbox, inline buttons, a link outside the
row, a loose Disconnect), and some rows were not settings at all (Last
push, Deploy now). Of four mocks, Saile picked "one column, one row
shape":

- No section list. One column, 860px at most; the sections keep their
  anchors for search and links.
- Every row is a label and a value that opens its form. Defaults show in
  place, marked "default", instead of folding into "N on defaults".
- Source is one "Deploys from" row (repository · branch · path) whose
  form changes each of them, links to GitHub and disconnects. The image
  row shows only when no repository is connected or an image is set.
- Last push moved to History; Deploy now is gone, since the header's
  Deploy already deploys a commit, branch or tag.
- Blind reviews earlier scored tab-only changes lower (0076's trials).
  This changes placement, not just tabs, and should be measured with
  "find X" tasks on the review kit.
