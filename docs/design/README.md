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

B1 and B2 are structure references (what the operator console holds, which
navigation each role sees); style them as A.

## Tokens

Measured from the mockups (most used first). Put them in Tailwind theme
variables once, not as literals in components.

### A: the app

| Token | Value |
|---|---|
| Sidebar background | `#0a0a0b` |
| Page background | `#0e0e10` |
| Panel, selected row | `#151517`, `#1c1c1f` |
| Lines | `#232326` (dividers), `#1c1c1f` (subtle), `#2e2e33` (controls) |
| Text | `#ededef` primary, `#c8c8cf` secondary, `#a1a1aa` muted, `#8b8b94` faint |
| Live, healthy | `#4ade80` (dot and text), `#14261b` tint |
| Staged, warning | `#fbbf24`, `#fcd77a` text, `#2a2210` / `#17140b` tints, `#3a2f12` border |
| Failed | `#f87171` |
| Links, info | `#93c5fd` |
| Sans | Inter (weights 400, 500, 600) |
| Mono | JetBrains Mono (ids, hashes, images, logs, values) |
| Sizes | 12px body, 11px meta, 13px emphasis, 14 to 20px titles |
| Radius | 6px controls and rows, 8 to 10px panels, 999px dots and badges |
| Layout | sidebar `flex: 1 1 248px` with a workspace switcher and project tree; main column with a 10px 20px header bar and 24px 28px content, max width 1100px |

### G: data panel and tempoBase

| Token | Value |
|---|---|
| Page background | `#0a0a0a` |
| Sidebar | `#0f0f0f`, `flex: 1 1 240px; max-width: 264px` |
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
- The near-black backgrounds above replace the old true black `#000`.
- Keyboard first: every action reachable without a mouse, visible focus.

## Fonts and performance

A uses Inter and JetBrains Mono. Ship them self-hosted as Latin-subset
woff2 with `font-display: swap`, loaded by the shell CSS, not by route
chunks, and count them in the initial-load budget (`make perf`). Fall back to
the system stacks. G uses system fonts only, which keeps tempoBase's
first load small.
