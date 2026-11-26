# 0105: GitHub Apps of the platform or an org

Status: accepted (Saile, 2026-11-26). Does what
[0066](0066-github-apps-for-many-orgs.md) left as "Next: an org's own
App"; supersedes in part [0031](0031-github-app.md)'s one App per install.

## Context

Dock had one GitHub App ([0031](0031-github-app.md)), private by default.
GitHub lets only the account that owns a private App install it, so with
a private App under `nordtempo`, nothing in `sailedev` could deploy
through it. The ways out were making that App public (every org shares
one App, one name on statuses, one key) or a webhook and token per
service. Some GitHub orgs also refuse third-party Apps, or want their own
name on statuses and comments.

## Decision

An App has an owner: the platform (`org_id` NULL) or one org, as
Cloudflare connections do ([0062](0062-org-alerts-and-operators.md)).
Any number of Apps per owner, up to 10.

| Topic | Platform App | Org App |
|---|---|---|
| Who manages it | Operators (`dock github app create --platform`) | The org's unlimited admins (`dock github app create`) |
| Public or private | Either; private by default | Private only |
| Whose installations | The org each is assigned to: a verified install link, or `dock github assign` (0066) | The owning org's, when on the App's owner account (by GitHub account ID) |
| Checking installs | OAuth check (0066) | None: GitHub says which account the installation is on |
| When it fails | Platform alert `github:app:<id>` | The org's alert `github:app:<id>` |

- **Recording an org App's installation.** Two paths, both GitHub's word:
  GitHub's setup redirect after an install from Dock's link, where Dock
  asks GitHub as the App (`GET /app/installations/{id}`) which account it
  is on, and the App's signed `installation` webhook (`created`). Neither
  trusts the redirect's `installation_id`. An installation on another
  account (the App made public on GitHub later) counts for nothing.
- **Connecting.** The org's own Apps are asked first, oldest first, then
  the platform's. The first whose installation reads the repository and
  is the org's wins. An org that may install several Apps names one
  (`--app`).
- **Pushes.** Each App's webhook has its random `hook_id`, so deliveries
  find their App by it. A connection names one installation, so a
  repository two Apps read deploys once. An App's uninstall event only
  forgets its own installations.
- **Listing repositories** walks the org's installations, each with its
  own App's token, and lists a repository two of them read once.
- **Store.** Migration 0134 turns `github_app` into `github_apps`
  (`org_id`, `owner_id`) with the existing App as the platform's App 1,
  and gives `github_installations` an `app_id`. `git_connections` doesn't
  change. An App's sealed values are bound to its `hook_id`; the migrated
  App's, sealed for the one-row table, are bound on the next start
  (`resealGitHubApps`, secrets format 3).
- **API.** `POST /github/apps` (`platform=true` for the platform's),
  `DELETE /github/apps/{slug}`, `PUT /github/apps/{slug}/client-secret`
  (platform Apps). `GET /github` and `GET /github/org` list `apps`; their
  old single-App fields stay, filled from the first platform App, so
  `--json` output only gains fields.

## Why this shape

**Org Apps need no claim check.** A private App installs only on its
owner account, and Dock checks the account ID too, so an org App's
installation can belong to no other org. 0066's install binding, OAuth
check and confirm step guard platform Apps that anyone may install; an
org App doesn't need them.

**Same model for both owners.** A cap of one platform App would be a rule
to explain and test for little: an install that wants two GitHub
accounts' Apps for every org gets them the same way an org does.

**Own App first.** An org that registered its own App wants its name on
statuses and its key used, even where the platform's public App is
installed on the same account.

## Not now

Hiding platform Apps from orgs (`[github] platform_app = false`), public
org Apps, moving an App between owners, GitHub Enterprise hosts.

## Performance

Idle: nothing new. Per delivery: one indexed lookup by `hook_id`, as
before. Per installation token: one indexed read of its App, only when
the cache misses (tokens last an hour). Connecting a repository: at most
one GitHub call per App the org may use.

## Consequences

- An org deploys from its own GitHub account with its own private App,
  with no operator involved.
- Removing an App no longer replaces "the" App: services connected
  through it stop fetching until they reconnect or another App's
  installation delivers for their repository (adoption, 0031).
- `dock github app remove` and `secret` take the App's slug.
