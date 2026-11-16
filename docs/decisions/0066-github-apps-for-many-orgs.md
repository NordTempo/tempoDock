# 0066: GitHub Apps for many orgs

Status: accepted (asked by Saile, 2026-11-08; decided by the agent)

## Context

Dock has one GitHub App per install ([0031](0031-github-app.md)), and
it is private. GitHub only lets the account that owns a private App
install it. So every Dock org has to deploy from that one GitHub
account. A platform that hosts several companies, or an open source
install that someone runs for friends, needs orgs to bring their own
GitHub accounts.

Since [0062](0062-org-alerts-and-operators.md), an org's admins get an
install link for their own org. GitHub's redirect back carries the
`installation_id` in the URL, and Dock claims whatever ID the URL says.
GitHub warns against trusting that parameter. With a private App this is
a small risk, because only the owner account can install it. With a
public App, anyone can install it. An admin of org B could edit the URL
and claim an installation that org A made on GitHub directly and nobody
has assigned yet. [0065](0065-sign-in-is-the-platforms.md) lets people
sign up on their own, which makes this worse.

Some GitHub orgs won't install a third-party App at all: their policy
blocks it, they want their own name on statuses and comments, or they
want to hold the key themselves.

| Force | Means here |
|---|---|
| Orgs are tenants ([0027](0027-organizations.md)) | Who installed an installation must be proven, not read from a URL |
| Open source, self-hosted | An install chooses: a private App for one team, a public App for many orgs, or (next) an App per org |
| Nothing breaks | The App on prod keeps working; turning on the check is a few steps for an operator, not a new App |
| Idle means idle | No polling, no stored user tokens to refresh |

## Decision

| Topic | Decision |
|---|---|
| Public or private | `dock github app create --public` (`public` in `POST /github/app`) registers a public App: any GitHub account can install it. Private stays the default. GitHub can make a private App public later (App settings > Advanced). It can't make a public App private once other accounts have installed it, so Dock doesn't choose public for you |
| Proving the installer | The App keeps its OAuth client: `client_id`, and `client_secret` sealed with the secrets box. When GitHub redirects back after an install, Dock doesn't claim the installation yet. It sends the browser on to GitHub's authorize page for the App (PKCE, a new single-use state that carries the org and the installation ID), at `/api/v1/github/user/callback`. Dock exchanges the code for a user token and asks GitHub `GET /user/installations`. It claims the installation only if it is in that list: the person who came back can access it on GitHub. The token is used for that one call and then dropped, never stored |
| Who is checked | Everyone, operators too, once the App has a client secret. Without one, an operator's link claims the installation as before (operators are trusted with the platform), and an org admin can't get an install link: "ask an operator to finish the App's setup: `dock github app secret`" |
| New Apps | The manifest asks for the callback URL (`callback_urls`), and Dock keeps the `client_id` and `client_secret` that GitHub returns along with the key. Nothing else to do |
| Apps made before this | An operator adds the callback URL Dock shows to the App's settings on GitHub, generates a client secret there, and gives it to Dock: `dock github app secret` (stdin) or `PUT /github/app/client-secret`. Dock reads the `client_id` from `GET /app` and checks the pair with GitHub (`POST /applications/{client_id}/token`: a made-up token gets 404 when the pair is right and 401 when it's wrong) before storing it. Operators only, audited |
| Status | `GitHubApp.verifies_installs` is set once installs are checked. The console and `dock github status` say so, or say what is missing |
| Installed elsewhere | A redirect without Dock's state (an install made on GitHub directly, or repositories changed there) claims nothing, as before. An operator assigns such an installation (`dock github assign`) |
| Migration | 0079: `github_app.client_id` and `client_secret` |

### Next: an org's own App

Same model as Cloudflare connections in 0062: an App is the platform's
or an org's.

- `github_app` becomes `github_apps` with `org_id` (NULL is the
  platform's). Its random `hook_id` already tells deliveries apart.
  `github_installations` gains `app_id`. Connections reach their App
  through their installation, so they don't change.
- An org's unlimited admins register a private App under their GitHub
  org with the same manifest flow. A private App can only be installed on
  that one account, so its installations belong to the org without a
  check.
- The org then sees both its own App and the platform's when it
  connects a repository. An operator can turn the platform App off for
  orgs (`[github] platform_app = false`) for installs where every org
  brings its own.

### Later: pull request feedback

Previews already post a `dock/preview` status and one comment, edited in
place ([0056](0056-preview-environments.md)). To show each service's
state in the pull request, the next steps are a row per service in the comment with
its state (building, ready, failed) and a link to its release, and
GitHub Deployments (`deployments: write`). With Deployments, the pull
request shows "View deployment" and the preview's environment on its
timeline. Each new permission makes every installation of a public App
approve it again on GitHub, so these come together, in one change of the
manifest.

## Why this shape

**Check with the user's token, not the webhook's sender.** The
`installation` event names who installed, but matching that GitHub user
to the Dock person in the browser would need their GitHub identity
linked to their Dock account. The authorize step proves both at once,
usually without a click: GitHub skips the consent page for someone who
has already authorized the App.

**Not "Request user authorization during installation".** With that
setting GitHub drops the setup URL and redirects to the callback with
only a `code`. The state, which carries the org, doesn't come back. The
second redirect is Dock's, so the state stays with it.

**Check operators too.** One path is simpler to test, and an
operator's link should not be the one an edited URL can bend.

**Don't store the user token.** Dock needs one answer. A stored token
lasts eight hours and comes with a refresh token, which would mean
sealing, rotating and expiring them for nothing.

## Not now

GitHub Enterprise hosts, org-owned Apps (above), connecting an
installation that already exists on GitHub without an operator, and
choosing the platform App's permissions from Dock.

## Performance

Idle: nothing new. Per install: one extra redirect, one token exchange
and one or two `GET /user/installations` pages (100 each, at most 10).
Memory: the verify links share the 16 slots of 0031's pending links.

## Consequences

- An install with a public App can host orgs from any GitHub account,
  and an edited redirect can't claim someone else's installation.
- Org admins on an App made before this can't get an install link until
  an operator adds the client secret. Their installations that are
  already assigned keep working.
- A person who can see an installation on GitHub (a member of the GitHub
  org with access) can claim it for a Dock org they administer, if no
  other org has it. That matches what GitHub lets them do with the App
  itself.

## Amendment (2026-11-15): an install link is its starter's

The state proved which org a redirect was for, not who followed it, and
the install pages needed no session. An admin of any org (anyone may
create one) could send their install link to a GitHub account's owner:
GitHub's redirects came back with the admin's state, `/user/installations`
showed the owner could access the installation, and Dock gave it to the
admin's org, which could then deploy, and so read, the owner's
repositories.

- `POST /github/installations` returns Dock's start page
  (`/api/v1/github/install/start?state=`), not GitHub's. The page needs
  the session of the person who asked for the link (the session cookie is
  `SameSite=Strict`, so it can't be read on GitHub's cross-site redirect
  back) and binds the flow to that browser with a cookie,
  `__Host-dock_github` over HTTPS (`SameSite=Lax`, an hour), then goes on
  to GitHub. Someone else's session, or none, gets no further.
- GitHub's install and authorize redirects and the confirm step each
  check the cookie: in another browser they claim nothing.
- After the check with GitHub, Dock shows "Assign to org X?", naming the
  installation's account as the App reads it, and claims the installation
  only when the person submits that form. Operators' links to an App
  without its client secret confirm the same way.
- Install links are a person's: a token with no user is refused, since
  it has no browser to finish in.
