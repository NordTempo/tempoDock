# 0031: Dock's GitHub App

Status: accepted (decided by the agent, on Saile's direction); amended by [0066](0066-github-apps-for-many-orgs.md): public Apps, installs checked against the GitHub user.

## Context

Push-to-deploy ([0018](0018-git-push-deploy.md)) gives every service its
own webhook and, for private repositories or commit statuses, its own
fine-grained token. Connecting a service means running `dock git
connect`, copying a URL and a secret into the repository's settings on
GitHub, and pasting a token. A monorepo with three services needs three
webhooks. Before any of that, the server needs `[hooks] public_host` in
`config.toml` and a restart.

0018 skipped a GitHub App because each Dock install would need its own
registered App, a private key, JWT signing and token exchange. GitHub's
App manifest flow registers an App in one click, and Dock now has a
public address set from the console ([0029](0029-public-address.md)).
That makes those costs one-off and small. The owner wants pushes to
deploy by default, with nothing to set up per service.

| Force | Means here |
|---|---|
| One App per install | Not one per org; orgs share it |
| Orgs are tenants ([0027](0027-organizations.md)) | An org must never read another org's repositories through the shared App |
| UI and CLI parity ([0026](0026-ui-cli-parity.md)) | Creating and installing the App works from the CLI too |
| Idle means idle | No polling, no token refresh timer |
| Nothing breaks | Webhook connections keep working unchanged |

## Decision

| Topic | Decision |
|---|---|
| How many | One App per Dock install, in the single-row table `github_app`. Operators create, show and remove it |
| Registration | GitHub's manifest flow. `POST /github/app` returns a page on Dock (`/api/v1/github/app/new?state=…`). The page posts the manifest to github.com (or to `--owner`'s org), GitHub creates the App and redirects to `/api/v1/github/app/callback`, and Dock exchanges the code for the App's ID, private key and webhook secret. The key and secret are sealed with the secrets box |
| The App | Private (only its owner account can install it). Permissions: Contents read, Metadata read, Commit statuses write. Events: `push`. Its webhook URL is `/hooks/github/<hook_id>` with a random hook ID, like a connection's |
| Installations and orgs | Each installation is assigned to one org (`github_installations`), by an operator. `dock github install <org>` gives the App's install link, carrying a state for that org; GitHub's redirect to `/api/v1/github/install/callback` assigns it. `dock github assign <id> <org>` covers installs made on GitHub directly. Uninstalling the App (the `installation` event, `deleted`) removes the assignment |
| Authority of the redirects | A state: 256 random bits, issued to an operator, single use, valid for an hour, in memory (at most 16). The redirects need no session, so the flow works when started from the CLI. A manifest state can't complete an install, nor the reverse |
| Tokens | An RS256 JWT signed with the standard library, exchanged for an installation token (one hour). Tokens are cached in memory per installation and renewed on use when less than 5 minutes are left |
| Connections | `git_connections` gains `installation_id` and `repo_id` (both 0 for webhooks). Mode `app` or `webhook`. When the org has an installation, `dock git connect` defaults to `app`; `--mode webhook` keeps the old way. App connections have commit statuses on by default |
| Org check | At connect time, the repository's installation must be assigned to the service's org. It is checked again every time a token is used, so unassigning or moving an installation takes effect at once |
| Routing pushes | One delivery reaches every App connection with that `(installation_id, repo_id)`, and each decides by branch and directory as before. Routing by the repository's ID survives renames. GitHub's delivery log gets one line per service |
| Public endpoint | `/hooks/github/` is now also served on Dock's public address, not only on `[hooks] public_host`. A public address alone is enough for webhooks of either mode |

## Why this shape

**Operators assign installations, not org admins.** An installation ID
is a small integer anyone can guess. If org admins could claim
installations, org B could claim org A's, unless Dock proved who
installed it through GitHub user OAuth. Dock is invite-only today, and its
operator is trusted with the whole platform anyway. When orgs sign up on
their own, self-serve installs come with that OAuth check.

**Check the org on use, not only at connect.** Assignments change.
Checking once would let a service keep reading a repository after its
org lost the installation.

**A random hook ID for the App, too.** The hooks listener refuses unknown
URLs before reading a body, so strangers can't hold its two read slots. A
fixed `/hooks/github/app` would give that up.

**Keep webhook connections.** They are tested, they need no App, and they
work for repositories on accounts the App can't be installed on.

## Not now

Check runs, pull request preview environments, GitHub Enterprise hosts,
rotating the App's key from Dock, updating the App's webhook URL when the
public address moves (`PATCH /app/hook/config`), and installs by org
admins.

## Performance

Idle: nothing new runs. No goroutines, timers or polling. Per App
delivery: one HMAC, one indexed lookup of the repository's connections,
then the same queries per connection as a webhook delivery. Per fetch or
status: one primary-key read for the org check, and at most one token
exchange per installation per hour. Memory: one token per installation
and at most 16 pending links.

## Consequences

- Connecting a service is choosing a repository, branch and directory.
  Private repositories and commit statuses need no token.
- Monorepos need one App, not one webhook per service.
- Removing the App, or uninstalling it on GitHub, stops its connections'
  fetches with a reason. They are reconnected after a new App is
  installed. Recreating the App doesn't move connections over, because
  installation IDs change.
- The `dock github` commands are operator commands except `repos`. The
  console has the same under System → GitHub, and Settings → Source
  suggests the org's repositories when connecting.
