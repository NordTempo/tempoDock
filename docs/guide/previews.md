# Preview environments

Every pull request can get its own environment, `pr-<number>`, cloned
from a base environment such as `staging`, built from the pull request's
head commit and given its own addresses. It follows new commits and is
archived, with its containers, volumes and addresses, when the pull
request closes ([ADR 0052](../decisions/0052-preview-environments.md)).

## Turn them on

```sh
dock env previews enable staging                          # pull requests into what staging deploys
dock env previews enable staging --max 5 --copy web.PUBLIC_API_URL --copy shared.SENTRY_DSN
dock env previews                                         # settings and open previews
dock env previews disable                                 # no new previews; open ones stay until they close
```

Or in the UI: the project's settings, Previews. Turning previews on needs
admin on the whole project: a preview runs a pull request's code.
`enable` replaces the settings, so pass every setting each time.

The base's services must deploy from GitHub ([deploy on git
push](deploying.md#deploy-on-git-push)). A pull request gets a preview
when it goes into the branch a base service deploys: with staging on
`main`, every pull request into `main` of that repository.

## What a preview is

| What | In the preview |
|---|---|
| Kind and server | Always non-production, on the project's non-production server. A server that only accepts production never runs one |
| Services and settings | The base's, like [`dock env clone`](deploying.md#clone-an-environment) |
| Code | Services connected to the pull request's repository build its head commit. Other services run the base's current image |
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
  database.
- **Every other value is left out**, unless you list it with `--copy`
  (`web.KEY`, or `shared.KEY` for a shared variable). Nothing is listed by
  default. List public, non-secret values (a public URL, a feature flag);
  set test credentials on the base under another name if a preview needs
  them.

Pull requests **from forks** never get a preview unless you pass
`--forks`. A fork is anyone on GitHub; with `--forks`, their code runs on
your server with the listed values.

## On the pull request

Dock posts a `dock/preview` commit status on the head commit (pending,
success with a link to the preview, failure) and one comment listing the
addresses, edited in place as the preview deploys, updates and closes.

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
