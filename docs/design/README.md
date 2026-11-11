# Design

The UI follows the **T3 style** mockups from Claude Design (2026-10-22).
They replace `ui-direction.html` (the earlier v2 direction) wherever the two
differ. Every new screen, and every screen migrated to the new look,
starts from these files.

| Where | Style | Mockups |
|---|---|---|
| Dock: projects, services, variables, members, servers, admin | A (T3 style) | `t3-style/app/` |
| Data panel in Dock, and tempoBase standalone (ADR 0024) | G (T3 style) | `t3-style/data/` |

## Files

- `source/` holds the originals as received. Open them in a browser; each
  is one self-unpacking page with every board.
  - `tempo-dock-redesign.html`: A1 to A5 (T3 style), B1 and B2 (platform
    console, who sees what), C1 to C3 (alternatives not chosen).
  - `database-viewer-styles.html`: A to F (alternatives not chosen), G1 to
    G9 (T3 style).
- `t3-style/` holds the chosen boards as plain HTML with inline styles,
  extracted from the originals, so they can be read without unpacking.
  The `<x-dc>` and `<helmet>` wrappers come from the design tool; ignore
  them. Font files are not included (see Fonts).

| Board | File |
|---|---|
| A1 Project overview | `t3-style/app/A1-project-overview.html` |
| A2 Service: deploys and live logs | `t3-style/app/A2-service-deploys-logs.html` |
| A3 Variables with staged changes | `t3-style/app/A3-variables-staged.html` |
| A4 Workspace members and roles | `t3-style/app/A4-members-roles.html` |
| A5 Project graph view | `t3-style/app/A5-project-graph.html` |
| B1 Platform console (operators only): servers | `t3-style/app/B1-platform-console-servers.html` |
| B2 Who sees what: navigation and roles | `t3-style/app/B2-navigation-and-roles.html` |
| G1 Table and row details | `t3-style/data/G1-table-row-details.html` |
| G2 All tables | `t3-style/data/G2-all-tables.html` |
| G3 SQL editor | `t3-style/data/G3-sql-editor.html` |
| G4 Edit mode and review | `t3-style/data/G4-edit-mode-review.html` |
| G5 Everything about one record | `t3-style/data/G5-everything-about-one-record.html` |
| G6 Schema map | `t3-style/data/G6-schema-map.html` |
| G7 Agents and MCP | `t3-style/data/G7-agents-mcp.html` |
| G8 Access | `t3-style/data/G8-access.html` |
| G9 Activity | `t3-style/data/G9-activity.html` |

Three later files hold the mocks a feature's direction was picked from:
`environment-lock.html` (ADR 0044), `jobs.html` (ADR 0054) and
`run-command.html` (ADR 0048). `ui-direction.html` is the superseded v2
direction.

B1 and B2 are structure references (what the operator console holds, which
navigation each role sees); style them as A. The console Dock builds is
B2 for operators ([ADR 0025](../decisions/0025-platform-console.md),
[ADR 0027](../decisions/0027-organizations.md)); the workspace switcher
of the boards is the organization switcher.

## Tokens

Measured from the mockups (most used first). Put them in Tailwind theme
variables once, not as literals in components.

### A: the app

| Token | Value |
|---|---|
| Sidebar background | `#000`, set off by a 1px line |
| Page background | `#000` |
| Panel, hover, selected row | `#0b0b0c`, `#141416`, `#1c1c1f` |
| Lines | `#1f1f22` (dividers), `#161618` (subtle), `#2c2c30` (controls) |
| Text | `#fafafa` primary, `#d0d0d6` secondary, `#a1a1aa` muted, `#8b8b94` faint |
| Live, healthy | `#4ade80` (dot and text), `#14261b` tint |
| Staged, warning | `#fbbf24`, `#fcd77a` text, `#2a2210` / `#17140b` tints, `#3a2f12` border |
| Failed | `#f87171` |
| Links, info | `#93c5fd` |
| Sans | Inter (weights 400, 500, 600) |
| Mono | JetBrains Mono (ids, hashes, images, logs, values) |
| Sizes | 24px page titles, 16px section headings, 14px body, 13.5px meta and hints, 13px table heads |
| Radius | 6px controls and rows, 8 to 10px panels, 999px dots and badges |
| Layout | sidebar 248px with the organization switcher, search, project tree and the organization's pages; main column with a 52px header bar and 24px 28px content, one width for every page (max 1400px) |
| Root size | 14px, so Tailwind's spacing scale is 14/16 of the usual: a size that must be exact (24px targets) is written in px |

### G: data panel and tempoBase

| Token | Value |
|---|---|
| Page background | `#000` |
| Sidebar | `#000` with a 1px line, `flex: 1 1 240px; max-width: 264px` |
| Raised surfaces | `rgba(255,255,255,.03)` to `.05`, `#191a1d` popovers |
| Lines | `rgba(255,255,255,.06)` dividers, `.08` controls, `.04` grid |
| Text | `#f5f5f5` primary, `#a3a3a3` secondary, `#8a8a8a` muted, `#525252` faint |
| Accent | `#346bf1` (primary buttons, selection `rgba(52,107,241,.12 to .16)`), links `#7da2ff`, hover `#a8c0ff` |
| Added, removed | `#86efac`, `#fca5a5` |
| Sans | system stack: `-apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif` |
| Mono | `ui-monospace, 'SF Mono', Menlo, monospace` (cells, SQL, keys) |
| Numbers | `font-variant-numeric: tabular-nums` everywhere |
| Sizes | 12 to 13px body, 11px meta, 14px titles |
| Radius | 8px controls, 10px panels, 4px small tags, 9999px pills |
| Heights | 28 to 32px rows and controls, 36px header controls |

Inside Dock, the data panel keeps G's grid, editor and review screens but
sits in A's shell (sidebar, header, colours for status). tempoBase uses G
for everything.

## Rules that still apply

- Dense, quiet, no decorative card chrome, minimal copy, no em dashes.
- No continuously repainting animations (spinners, pulse, shimmer).
- True black (`#000`) pages in both styles; panels, hover and selection
  are the few raised steps above it (2026-11 UI pass).
- One confirmation: the kit's `Confirm`, a small modal that asks the
  question ("Roll back to r7?"), says what happens in plain words and names
  the action on its button. The safe button has focus. `typed` (type the
  name) only for destructive changes that can't be undone.
- Feedback: `notify()` (ui/notice.tsx) says an action went through; static,
  gone after a few seconds. Dialogs end in one footer: the error or why it
  can't submit on the left, Cancel and the named action on the right.
- Keyboard first: every action reachable without a mouse, visible focus.

## Structure (2026-11 redesign)

- **Navigation.** The sidebar holds the organization switcher, one search
  (the palette, Cmd+K: pages and `dock` commands in one list), the project
  tree (and archived projects), then Inbox (open alerts and, for admins,
  access requests to decide), Errors, Analytics, Images, Status pages,
  Activity, Organization settings (members, tokens and where alerts go),
  Help, the account and, for operators, the console (ADR 0076). There is
  no second command bar.
- **Pages.** An organization lists its projects; a project page shows its
  problems, one table per environment and its activity; an environment's
  address (`/org/project/env`) is that environment's own page. A service
  has one header (name, environment, state, actions) and its tabs in four
  groups with a line between them: watching it (Overview, Logs,
  Resources, Errors, Requests, Uptime), operating it (Jobs, One-off
  command, Data, Backups), configuring it (Domains, Variables, Build &
  run), its history (Activity). Tabs every service has come first; the
  list is `web/src/features/service/tabs.ts`. ADR 0076 merges these into
  nine tabs screen by screen; a tab's views (Requests' Traffic and
  Visitors) are underlined links under it, each with its own URL.
- **Overview.** One column: status, facts, uptime and visitors, how to
  connect, the deploy in flight, releases. Logs have their own tab.
- **Width.** Every page uses the one content width. Below 1400px, side
  columns (members' roles, analytics' side panel) move under the main one.
- **Help.** `/org/help` explains Dock's words; "What's this?" links point
  into it from where a concept appears.

## Patterns

- **Keys mean one thing everywhere.** N adds to the page's list (service,
  variable, domain, member, token, server), S opens settings, / filters,
  D deploys, R restarts, B rolls back, digits pick the nth item or view, G
  then a letter switches tabs, ? lists the keys, Cmd+K searches.
- **One primary per view.** The header's Deploy is primary until changes
  wait to apply; then Apply in the bar is. Page actions next to it are
  secondary.
- **Changes wait to apply.** Variables, settings, domains and volumes are
  saved first ("waiting to apply") and go live together with Apply; the
  strip under a service's tabs stays one line until opened.
- **Settings rows.** A label with a short line under it, the values as
  text, and one Edit per row that opens a small form; Save, Cancel, Esc.
- **Tables and lists** sit on the page under a hairline, with no box; status in tables and headers is a tinted chip; the page header is two lines, the way up over the title (chosen in blind direction trials, 2026-11).
- **Rows that open something** show a pointer, a hover and a chevron in
  their last cell; the whole row is the target.
- **One component vocabulary.** An action in a row or list (Reveal, Edit,
  Retry, "Show all") is a small bordered button like every other button;
  only inside running text is it a link. Segmented choices (ranges,
  views) are underlined like the section tabs. Key hints stay on buttons
  (a blind trial that hid them lost discoverability).
- **A fresh failure** on a service's overview is a red-tinted strip with
  a ruled left edge under the status line, the way to the cause as a
  button; elsewhere it is said once, in the project's problems.
- **Destructive buttons** are red outlined on pages and filled red in the
  confirmation that runs them; every removal that can't be undone asks
  first, saying what stops working. A confirmation leads with what is
  lost.
- **Disabled** looks the same on every button: outlined, faint text, with
  the reason next to it where there is one. A destructive one stays dim
  red, so it still reads as dangerous.
- **Fields** (inputs, selects, checkboxes) have a 3:1 edge on black.
- **Targets** are at least 24px: buttons, inputs, selects and standalone
  links (by padding taken back with a negative margin).
- **Defaults** are said where they are chosen (Build & run reads "1 CPU
  (default)"); in tables the value alone, dimmer, with a tooltip.
- **Words.** Plain words over product terms ("Going live", "Before it goes
  live", "Checks while running"): replaced (not superseded),
  traffic moved, downtime, waiting to apply, reached at, memory reserved,
  "1 CPU (default)".

## Fonts and performance

A uses Inter and JetBrains Mono. Ship them self-hosted as Latin-subset
woff2 with `font-display: swap`, loaded by the shell CSS, not by route
chunks, and count them in the initial-load budget (`make perf`). Fall back to
the system stacks. G uses system fonts only, which keeps tempoBase's
first load small.
