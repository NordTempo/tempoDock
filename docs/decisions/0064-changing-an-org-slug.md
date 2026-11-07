# 0064: Changing an organization's slug

Status: accepted (decided by the agent, on Saile's direction, 2026-11-07).
Amends [0027](0027-organizations.md) and [0062](0062-org-alerts-and-operators.md).

## Context

An org's slug is its address: `/<slug>/...` in the browser, `--org <slug>`
and `DOCK_ORG` in the CLI, `?org=` and `Dock-Org` in the API, and the org
CLI contexts and directory links remember. [0027](0027-organizations.md)
fixed it at creation, and an install's first org is `default`. Saile wants
to rename theirs (to `nordtempo`), and links, bookmarks, scripts and other
people's CLI contexts should not break when that happens.

## Decision

- **Who:** an org's unlimited admins (`PATCH /org`, `dock org rename
  --slug`, Organization settings, General), and operators for any org
  (`PATCH /console/orgs/{org}`, the console's Edit settings), as with the
  rest of [0062](0062-org-alerts-and-operators.md)'s operator actions.
  The request takes a new name, a new slug, or both.
- **Rules:** those of a new org: 1 to 32 lowercase letters, digits and
  dashes, unique, and not a top-level page of the web UI.
- **Audit:** `org.slug` in the org's log, and in the platform's too when
  an operator does it.
- **Old slugs keep working.** `org_old_slugs` (migration 0077) maps every
  slug an org had to the org. Lookups by slug try the current slugs first
  and ask the table only on a miss, so a current slug costs no extra
  query: a session's or `dock login` token's org, a manual token's
  `org` check, the console's org lookups, single sign-on starts, user
  creation and GitHub installs. Old slugs map to the org, not to the next
  slug, so a chain of changes keeps every old one working.
- **Released when taken:** a slug in the table is removed when an org
  takes it (a new org, or an org changing to it, including back to its
  own old one). From then on it names that org; the previous org's
  members and tokens get not found or forbidden through it, as for any
  org they aren't in.
- **Web:** a URL whose first segment isn't one of the caller's orgs asks
  `GET /org` with that slug; if it names one of theirs, the app redirects
  to the same page at the new slug, keeping the query and hash. Changing
  your own org's slug moves you to the new address. A live `org.slug`
  event refetches the switcher, so other members' next page under the old
  slug redirects.
- **CLI:** `dock org rename --slug` rewrites this machine's contexts and
  directory links for the same server that named the old slug; `dock org
  use <old>` resolves to the new one. Elsewhere old slugs keep working
  through the server.
- **Everything else stores the org's ID** and joins for the slug, so it
  follows: memberships, tokens, invites and their links (the code, not the
  slug), GitHub installations, analytics sites and public dashboards
  (their own slugs), alerts, audit events and single sign-on sessions.
  URLs Dock builds (GitHub statuses, preview comments) take the slug when
  sent; ones sent before keep working through the redirect.

## Consequences

- One small table and one index; nothing runs while idle, and no request
  with a current slug does more work.
- A released slug sends old links to another org's not-found page. That
  is the cost of letting slugs be reused; holding them forever would let
  one org squat on many names.
- MCP has no org settings tools, so nothing changes there.
