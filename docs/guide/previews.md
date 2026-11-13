# Preview environments

Every pull request can get its own environment, `pr-<number>`, cloned
from the environment of the services it changes, such as `staging`,
built from the pull request's head commit and given its own addresses. It
follows new commits and is archived, with its containers, volumes and
addresses, when the pull request closes
([ADR 0056](../decisions/0056-preview-environments.md),
[ADR 0077](../decisions/0077-previews-belong-to-services.md)).

## Turn them on

Previews belong to services: a service that deploys from GitHub turns
them on, and its environment is the base the preview is cloned from.

```sh
dock service previews on -s web -e staging                 # pull requests into staging/web's branch
dock service previews on -s web -e staging --copy PUBLIC_API_URL
dock service previews -s db -e staging --seed              # each preview's db starts from its newest snapshot
dock env previews set staging --max 5 --sleep-after 1h --copy SENTRY_DSN
dock env previews                                          # who builds pull requests, open previews
dock service previews off -s web -e staging                # no new previews; open ones stay until they close
```

Or in the UI: the service's Settings › Source, Previews; the base's
numbers on the project's settings, Previews. Changing them needs admin on
the environment: a preview runs a pull request's code.

A pull request gets a preview when it goes into the branch a service with
previews on deploys, and changes something in that service's directory
(as a push does). In a monorepo, `web` (`apps/web`) and `api`
(`apps/api`) can both have previews on: a pull request changing both gets
one preview where both build its code; one changing only the docs gets
none. One repository and branch previews from one environment of a
project. A disconnected service builds no more pull requests.

## What a preview is

| What | In the preview |
|---|---|
| Kind and server | Always non-production, on the project's non-production server. A server that only accepts production never runs one |
| Services and settings | The base's, like [`dock env clone`](deploying.md#clone-an-environment) |
| Code | Services with previews on that deploy the pull request's repository and branch build its head commit. Other services run the base's current image |
| Addresses | Each service with a domain in the base gets a generated one, `web-shop-pr-12.<base domain>` |
| Volumes | New and empty. Never backed up, and removed when the preview closes |
| Variables | See below |
| Domains, backups, releases | Its own; nothing of the base's |

### Secrets stay out

A pull request's code is not reviewed yet, so a preview gets no secret
nobody chose to give it:

- Values that are only references (`${{db.DATABASE_URL}}`) are copied:
  they point at the preview's own services. A reference to an org
  variable (`${{org.KEY}}`) is not: it reads the org's secret, so it is
  copied only when listed.
- Database templates get new credentials for the preview's own, empty
  database, unless it is seeded.
- **Seeded services** (`dock service previews --seed`) start with
  data: before a new preview's copy of the service first starts, its own
  new volume is filled from the base service's latest snapshot on the
  same server (`dock volume snapshot -s db`). A seeded template database
  keeps the base's credentials, which the copied data files need. The
  base's volume is never mounted. Without a snapshot it starts empty, and
  the preview's last event says why. Take snapshots of what you seed
  from: the seed is only as fresh as the newest one.
- **Every other value is left out**, unless the service lists it
  (`dock service previews -s web --copy KEY`), or its environment lists a
  shared one (`dock env previews set staging --copy KEY`). Nothing is
  listed by default. List public, non-secret values (a public URL, a feature flag);
  set test credentials on the base under another name if a preview needs
  them.

Pull requests **from forks** never build a service unless it allows them
(`dock service previews on --forks`). A fork is anyone on GitHub; with
`--forks`, their code runs on your server with the listed values.

## On the pull request

Dock posts a `dock/preview` commit status on the head commit (pending,
success with a link to the preview, failure) and one comment listing the
addresses, edited in place as the preview deploys, updates and closes.
It also says when the preview sleeps (see [Sleeping](#sleeping)), so a
slow first visit doesn't surprise a reviewer; it isn't edited each time
the preview sleeps or wakes.

Through Dock's GitHub App this needs the `pull_request` event and Pull
requests write access. An App created before previews has neither: on
GitHub, open the App's settings, Permissions & events, add Pull requests
(read and write) and the Pull request event, then accept the new
permissions on each installation. Without write access the status still
posts and `dock env previews` says why there is no comment. Through a
webhook, tick "Pull requests" as well as "Pushes", and give the
connection's token Pull requests write access for the comment.

## Updates and closing

- A new commit (`synchronize`) redeploys the preview's git services.
  Pushes to the pull request's branch never reach it otherwise.
- Closing or merging archives it: services stopped, network, volumes,
  variables and addresses removed, and the environment hidden. Its history stays in the
  audit log. A deploy still running finishes first.
- `dock env previews close pr-12` does the same by hand. Reopening the pull
  request (or a new commit to it) makes a new preview.

Events are safe to deliver twice or late: an event older than the last one
applied changes nothing.

## Sleeping

A preview nobody uses goes to sleep after 30 minutes without requests: its
services stop (databases too) and give their memory back, and the
environment reads `asleep` with the memory it freed. The next request to
one of its addresses wakes it: the request waits a few seconds while the
services start again, databases first. A browser that waits more than 3
seconds sees a plain "waking up" page that reloads itself; other
requests (an API call, a webhook) wait up to 30 seconds. A deploy, a new
commit, `dock run` and the Data panel wake it too, and an open websocket
keeps it awake. Uptime checks pause while it sleeps and never wake it.

- `dock env wake pr-12` wakes it and waits; `dock env sleep pr-12` puts
  it to sleep now.
- `dock env previews set staging --sleep-after 2h` changes the time for
  the previews cloned from staging, open ones included; `--sleep-after
  off` keeps them up.
- Other non-production environments can sleep too: "Sleep after…" in the
  environment's More menu, or `dock env sleep-after staging 2h`.
  Production never sleeps.

Sleeping keeps every container, so a preview comes back as it was, except
for what its apps kept in memory. On a server whose dock is older than
the control plane's, previews stay awake until it is updated.

## Locks

A preview starts unlocked, even when its base is locked: it is
throwaway, and it never reaches the base's data, unlisted values or
domains. Lock one (`dock env lock pr-12`) and its pull request's new
commits wait for approval like any push into a locked environment.

`dock project export` leaves previews out, and `dock apply` never
touches them.

## Limits

| Limit | Default |
|---|---|
| Open previews per project | 3 (`--max`, up to 20). Beyond it the pull request gets an error status saying so |
| Disk | No new preview goes to a server whose disk is past `[retention] prune_disk_percent` |
| Forks | Off |
| Copied values | None |

## For agents

`get_previews` (a project's previews) and `get_preview` (by environment,
pull request number or branch) over [MCP](agents.md), or `GET
/api/v1/projects/{project}/previews`, give a preview's addresses and
status: `waiting`, `deploying`, `live`, `failed` or `closed`.
