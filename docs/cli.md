# CLI conventions

What scripts, CI and agents can rely on. The goals behind these are in
[experience](experience.md).

## Output

- Results go to stdout. Errors, warnings and progress go to stderr.
- `--json` works on every command. stdout is then exactly one JSON value
  whose shape is part of the command's contract: fields are only ever added,
  never renamed or removed. API objects are printed as the API returns them
  (`internal/apitypes`). Stored tokens are never printed; the one exception
  is the secret `dock token create` returns, once.
- Streaming commands (`dock logs`, `dock db query`) print one JSON object
  per line instead.
- Progress (deploy steps, build output) goes to stderr and is silent with
  `--json`.
- With `--json`, an error is one JSON object on stderr:

  ```json
  {"error": {"code": "not_found", "exitCode": 4, "message": "context \"x\" not found", "hint": "Run `dock context list` to see contexts."}}
  ```

  `code` is the server's machine-readable reason when it gave one (e.g.
  `health_check_failed`), otherwise the exit code's name from the table below.
- Human output uses aligned columns and plain status glyphs (`✓ ✗ ! ·`).
  Color only when the stream is a terminal and `NO_COLOR` is unset.
- The CLI only prompts when stdin is a terminal. Otherwise missing input is a
  usage error naming the flag to pass, and commands that ask for
  confirmation (`discard`, `domain remove`, `service archive`, `project archive`,
  `user delete`, `server revoke`, `server remove`, `alerts channel remove`, `storage remove`, `cloudflare remove`, `registry migrate`, `server prune`, `backup move`, `db setup`) need `--yes`, and commits to production data
  need `--confirm <environment>`.

## Exit codes

Stable; never renumbered.

| Code | Name | Meaning |
|---|---|---|
| 0 | `ok` | Success |
| 1 | `error` | Anything not covered below |
| 2 | `usage` | Bad flags, arguments or input. Nothing was done |
| 3 | `auth` | No server configured, no token, or the token lacks permission |
| 4 | `not_found` | The project, service, context, link, ... doesn't exist, or `promote` found nothing to promote |
| 5 | `conflict` | Already exists, another operation (e.g. a deploy) holds the lock, or the environment is locked (`environment_locked`) |
| 6 | `deploy_failed` | Build or health check failed. Traffic was not switched |
| 7 | `unreachable` | Could not reach the server (or its proxy answered 502/503/504), or the server an environment is placed on is offline (`server_offline`) |
| 8 | `incompatible` | CLI and server versions don't work together |
| 9 | `moved` | `dock serve` only: this control plane moved to another server (`dock admin backup --move`); systemd doesn't restart it |

API errors are RFC 7807 `application/problem+json` bodies. The exit code
follows the HTTP status (400/422 → 2, 401/403 → 3, 404/410 → 4, 409/412/423 →
5, 426 → 8, 502/503/504 → 7, else 1), except that these problem `code`
values take precedence: `build_failed`, `deploy_failed`,
`health_check_failed` → 6, `version_incompatible` → 8.

`dock db query` can also fail after its result started, with an error the
database reports (code `query_failed`): SQL it refuses as invalid (SQLSTATE
class 42) → 2, a write in the read-only transaction or a missing privilege
(25006, 42501) → 3, anything else → 1. A changeset whose rows changed since
it was proposed (`conflict`) → 5.

## Contexts and environment

A context is a named server URL plus API token, like a kubectl context:
`dock context add|list|use|current|remove`, and `dock logout` removes
the token of the context in use (and revokes it, if `dock login` got it in
the browser).

The server and token come from, in order:

1. `DOCK_URL` and `DOCK_TOKEN`. Contexts are ignored entirely, so no
   config file is needed (CI, agent sandboxes).
2. The context named by `DOCK_CONTEXT`.
3. The context of the linked directory (see below).
4. The current context.

A token only ever goes to the server it was given for: `DOCK_TOKEN` is
used only together with `DOCK_URL`. Setting `DOCK_TOKEN` alone is a usage
error (exit 2) rather than sending it to a context's server. The client
never follows redirects either; a 3xx is an error naming its target.
`dock context current` shows what the current directory resolves to and
why.

Log in with `dock login <server-url>` (no URL: the current context's
server). On a terminal it logs in through the browser, so there is no token
to create or paste:

1. The CLI prints a link and a code like `BCDF-GHJK` and opens the link
   (not over SSH or without a display; `--no-browser` only prints it, which
   also works without a terminal).
2. In the browser, sign in if needed and type the code from the terminal.
   The link never contains the code, so a link someone else sends you
   can't approve their login. The page then shows the CLI's host, OS and
   IP: deny a login you didn't start. A few wrong codes lock code entry
   for a few minutes.
3. The CLI gets its own token: named `cli: <hostname>`, acting with your
   role in each organization, valid for 90 days, listed on your account
   page (and the organization's Tokens settings) as from `dock login`.
   Codes expire after 10 minutes; Ctrl-C cancels.

The token acts for you, so it is only as good as your account: it stops
working when you are disabled or deleted, never has more access than your
current role, and changing or resetting your password revokes it (except
the token that changed it with `dock me password`).

`dock logout` revokes a token from a browser login on the server, and
logging in again replaces (and revokes) it. A denied or expired login exits
3. The browser and the CLI may be on different machines: the CLI never
listens on a port, it waits on the server.

Agents, CI and scripts use API tokens instead and pipe them in, so they
stay out of shell history and the process list:

```sh
dock login https://dock.example.com --token-stdin < token-file
```

`--token <token>` still works but is not recommended. Without a terminal
and without a token flag (or `--no-browser`), `dock login` is a usage error.

## Organizations

Projects and API tokens belong to an organization, and every command acts
in one, with your role there ([ADR 0027](decisions/0027-organizations.md)).
The organization comes from, in order:

1. `--org <slug>`.
2. `DOCK_ORG`.
3. The linked directory's: `dock link` records the organization the
   project is in.
4. The context's, chosen with `dock org use <slug>`.
5. None: an API token acts in its own organization, and a user in their
   only one. A user in several gets a usage error naming `--org`.

An API token never acts in another organization than its own.

## Config file and links

`$XDG_CONFIG_HOME/dock/config.json` (default
`~/.config/dock/config.json`), mode 0600, replaced atomically on write.

`dock link` records which project, environment and service a directory
deploys to. Links are stored in the config file by absolute path, not in the
repo. Commands use the link of the current directory or its nearest linked
parent, like git finds its repository. `dock unlink` removes that link.

Project, environment and service come from the link, and `--project/-p`,
`--environment/-e` and `--service/-s` override it. A `--project` other than
the linked one drops the linked environment and service; `-e staging` alone
keeps the linked service, so it means "this service, in staging".

A linked directory resolves its project by stable ID before a targeted command.
After a project rename it follows the same project, even if someone reuses the
old name. Explicit `-p` flags still address names. If the identity is missing or
inaccessible, run `dock link -p <project>` rather than falling back to the old
name. Update older CLI clients before renaming projects they use.

## Commands

`dock <command> --help` has the details.

| Command | What it does |
|---|---|
| `login [url] [--no-browser \| --token-stdin]` | Log in through the browser (on a terminal, or with `--no-browser`), or verify a token from stdin; store it as a context and use it. Warns when the server runs another version |
| `version` | The CLI's version and, when a server is configured, the server's (one unauthenticated request; nothing without a server), with the command that installs the matching CLI when they differ |
| `logout` | Remove the context's token; revoke it on the server if it came from a browser login |
| `whoami` | Who the token belongs to, the organization it acts in and its scope there |
| `org list\|create <slug> [--name N]\|use <slug>` | Your organizations and your role in each; create one (users, not tokens) with you as its admin; act in one from now on, in the context in use |
| `org members`, `org member role <email\|id> <role>\|remove <email\|id> [-y]` | The organization's members (admin); change a role (it always keeps an admin); take someone out (their account stays) |
| `org invite <email> [--role R]`, `org invites [revoke <id>]`, `org join <link\|code>` | Invite an email address (admin): prints a link that works once, for 7 days, only for that email; list or revoke open invites; accept one |
| `org env list [--reveal]\|set KEY=value...\|unset KEY...\|redeploy KEY` | Organization variables, used as `${{org.KEY}}` ([guide](guide/variables.md#organization-variables)): set and reveal (admin), remove one nothing uses, redeploy the running services that use one |
| `org rename <name>`, `org leave [-y]`, `org delete [-y]` | Rename (admin; the slug stays); leave (the last admin can't); delete an organization without projects (admin) |
| `mcp [--show-token] [--name N]` | Print the MCP client config for the context's server ([agents](guide/agents.md#mcp)): the endpoint URL, a `claude mcp add` command and an `mcp.json` entry. The token is `${DOCK_TOKEN}` unless `--show-token` |
| `me password [--password-stdin]` | Change your own password: prompts for the current one and the new one twice, or reads both from stdin (one per line). Needs a browser-login token, which acts for you; API tokens have no password. Your UI sessions and other browser-login tokens end; the CLI's own token stays valid |
| `link [-p -e -s]`, `unlink` | Link the directory. Pickers on a terminal; otherwise `--project` (and `--environment` when there are several) |
| `deploy [dir] [--image ref] [-d]` | Pack and stream the directory (or deploy an image) and follow build, start, health check and cutover. A `--service` that doesn't exist yet is created. An image deploy sets the service's `image` setting; a source deploy clears it. Exit 6 when the release fails. When the environment's server is offline or revoked, a source deploy stops before packing anything (`server_offline`, exit 7) |
| `promote FROM TO [-s service...] [-d]` | Deploy the images serving in environment FROM to TO (same project), by digest, with TO's own variables and settings; nothing is rebuilt. One service at a time, stopping at the first failure. Services missing in TO (or with nothing running in FROM) are skipped and listed. Needs deploy on TO. Exit 6 when a release fails, exit 4 when nothing was promoted (every service skipped; the skipped list is still printed, and with `--json` the result still goes to stdout) |
| `status [-a]` | The service's state, releases, domains and staged changes; `-a` (or no service) for the whole environment. On a server that is offline or revoked the state is the last known one and says so ("server t3node is offline: last known running"; `server_offline`, `server` and `server_status` in the JSON status) |
| `logs [-f] [-n N] [--since] [-r N] [--build]` | Service output, or a build log. `-r N` reads an earlier release's output, kept on its server after its container was removed (`[logs]` limits) |
| `traffic [--range 24h\|7d\|30d]` | What the service's domains served: requests, errors, average time, bytes sent and the most requested paths, with IDs as `:id` ([ADR 0033](decisions/0033-traffic-statistics.md)). Visitors are in `dock analytics stats` |
| `releases`, `release list\|show N` | Release history and one release with its events |
| `rollback [N]`, `release rollback [N]` | New release from an earlier one; default the one that served before the active one. Says which release it copies and that release's image and config |
| `restart` | New release identical to the active one |
| `connect [service] [--port N] [--confirm ENV] [--show-url]` | Tunnel `127.0.0.1` (the service's port if free, else a free one) to the service's private port until Ctrl-C, for psql, TablePlus, redis-cli ([guide](guide/data.md#connecting-from-your-machine)). Needs data access level write with sensitive visible; production needs `--confirm`, and API tokens never get production. `--show-url` prints the connection URL with its password (audited). `--json` prints one object (`service`, `port`, `local`, `url`, ...) before serving. Exit 3 without access |
| `git connect OWNER/REPO [--mode app\|webhook] [--branch B] [--dir D] [--token-stdin\|--remove-token] [--status] [--rotate-secret]` | Deploy on every push to a GitHub branch ([guide](guide/deploying.md#deploy-on-git-push)). Through Dock's GitHub App when it is installed for the org (statuses on, no token), else, or with `--mode webhook`, with its own webhook: prints the webhook settings for GitHub and its secret, once (again only with `--rotate-secret`). Running it again changes the branch, directory, token, statuses or mode. The token is read from stdin only and never printed |
| `git status`, `git deploy [--ref R]`, `git disconnect [-y]` | The GitHub source: the App or the webhook URL, token set or not, queued push, latest delivery; queue the branch's head (or a SHA, branch or tag) now, even if live; remove the connection and its token |
| `github status`, `github app create [--owner ORG] [--open=false]\|remove [-y]`, `github install ORG [--open=false]`, `github assign ID ORG`, `github unassign ID` | Operator: Dock's GitHub App ([ADR 0031](decisions/0031-github-app.md)). `app create` and `install` print a link valid for an hour (on stderr with `--json`) and open it in a browser; GitHub sends the browser back to Dock. `status` lists the App's installations with their org; `assign` lets an org deploy from one |
| `github repos` | The repositories the org can connect through the App (deploy scope) |
| `env list [--reveal]` | Shared and service variables |
| `env set K=V... \| unset K... \| import FILE [--shared] [--deploy]` | Stage variable changes; `--shared` for environment-level, `--deploy` to apply right away |
| `changes [--reveal]`, `apply` | Show or apply (and follow the resulting releases) the environment's staged changes |
| `discard [KEY...] [-s service] [--shared]` | Drop staged changes: all in the environment; with `-s`, that service's own; with KEYs, those variables' (the service's, or shared with `--shared`) |
| `domain list [-w]\|add HOST [--redirect-to]\|generate\|check HOST\|remove HOST` | Domains; `list -w` adds the URL, last DNS check and what the hostname resolved to; `add` prints the DNS record and waits (`--timeout`, default 5m) for DNS and the certificate, then prints the URL. A custom hostname routes only once the TXT record `add` prints first (`_dock-challenge.HOST`) proves your organization controls it; none is needed under the server's base domain, from Cloudflare, or for an operator. `check` looks for it, DNS and the certificate at once ([ADR 0042](decisions/0042-domain-verification.md)) |
| `access`, `access public\|login`, `access password --username U [--password-stdin]` | Who can open the service's domains: anyone, visitors with a username and password (basic auth; prompted twice, or one line on stdin; empty keeps the current one), or members signed in to Dock who can read the service. Takes effect at once on every domain ([ADR 0030](decisions/0030-access-protection.md)) |
| `volume list\|add NAME PATH` | Volumes (staged) |
| `service list\|create\|show\|update\|archive` | Services; `update` stages only the settings passed (`--pre-deploy-command CMD` and `--pre-deploy-timeout 15m` set the command each new release runs before it gets traffic, e.g. migrations; `--pre-deploy-command ""` removes it, [ADR 0045](decisions/0045-pre-deploy-command.md)). `create NAME --template postgres\|redis` creates a database from a template and deploys it ([guide](guide/databases.md)) |
| `backup list\|run [-d] [--control-plane]\|restore --snapshot ID [--as NAME] [-d]\|schedule [--hour H\|--default-hour] [--on\|--off]` | A stateful service's backups, which run on their own: list runs and snapshots, back up now (waits; `--detach` doesn't), restore into a new service (template databases), change the daily schedule (operator). `run --control-plane` (operator) backs up the control plane itself into the repository, as it does daily. Exit 1 when the awaited run fails |
| `backup status [--reveal]`, `backup configure [--keep-daily N] [--keep-weekly N] [--keep-monthly N] [--hour H]`, `backup move --storage NAME [--path P] [--yes]` | Where backups go, the repositories holding them and the latest move, retention and the default hour, for the whole platform; `configure` (operator) sets retention and the hour and prints the repository password the first time (its `--repository` and key flags are deprecated and refused); `--reveal` (operator) shows the password again; `move` (operator) moves every backup into a folder of a storage connection's bucket, keeps them restorable and empties where they were ([ADR 0039](decisions/0039-backups-on-storage-connections.md)) |
| `project list [--all]\|create\|archive <name>`, `environment list\|create [--kind]` | Projects and environments (`env` is variables). `project archive` (admin) archives every service in the project, removes its environments' networks and hides it from `project list` (`--all` shows it). An environment's kind (production or non-production) defaults from its name and never changes |
| `request list [--status S]\|show ID\|wait ID [--timeout 30m]` | Access requests: changes a locked environment refused and filed for an admin to approve in the UI. Admins see their organization's, a token its own. `wait` exits 0 when it ran, 1 when it failed, 3 when rejected, 4 when expired, 5 when the timeout passed first. There is no approve: tokens never decide ([ADR 0044](decisions/0044-environment-lock.md)) |
| `org locks [--lock-production on\|off] [--request-ttl 24h]` | Whether new production environments start locked, and how long access requests wait (unlimited admin to change) |
| `environment lock [ENV] [--reason R]`, `environment unlock [ENV] [--for 30m]` (also `env lock\|unlock`) | Lock an environment (admin): tokens, this CLI included, can still read it but every change they try fails with `environment_locked` (exit 5). People in the web UI are not affected. `unlock` (admin) unlocks, or with `--for` opens it for a while; run with a token in a locked environment it is refused like any other change. `environment list` and `status` show the lock ([ADR 0044](decisions/0044-environment-lock.md)) |
| `environment clone FROM TO [--kind] [--deploy] [--copy-values] [-d]` (also `env clone`) | A new environment with FROM's services, settings, variables and new, empty volumes, placed like `environment create`. Reference-only values are always copied; other values only for admins, and between production and non-production only with `--copy-values` (admin, listed in the output); otherwise templates get new credentials and the rest are listed. Domains, git connections, releases, backups and staged changes are not copied. `--deploy` deploys FROM's images like `promote` ([ADR 0022](decisions/0022-clone-and-export.md)) |
| `project rename <current> <new>` | Rename an active project and its address (admin with whole-project access). IDs, running services, domains, volumes and release history stay unchanged. Old project URLs and explicit names in scripts need updating; linked directories follow the project |
| `project export [NAME] [--format toml\|json]` | The project's live config: environments, services, settings, volumes, domains, git connections and variable names, never values. Sorted, so it diffs well in git |
| `project pin <project> --kind production\|non-production --server NAME` | Operator: choose the server a project's environments of a kind go to, before any exist |
| `server list`, `server show NAME` | Servers: link status (`online`, `offline`, `incompatible`, `joining`, `revoked`), version, ingress, accepted kinds, whether it builds, memory reserved; operators see every server, others the ones their organization's environments run on. `show` (operator) adds base domain, DNS target, what's placed and current usage |
| `server disk NAME`, `server prune NAME [--yes]` | Operator: where a server's disk goes (services' volumes, images by what keeps them, BuildKit's cache, Dock's own, the rest) and what a prune frees, read on demand (seconds); `prune` removes images no release needs and build cache above `build_cache_keep_mb` now ([ADR 0035](decisions/0035-server-disk-breakdown.md)) |
| `server add NAME [--production] [--non-production] [--builder]` | Operator: create a one-time join token (valid for an hour) and print the exact `install.sh --node` command to run on the new server. The token is shown once |
| `server update NAME [--production\|--no-production] [--non-production\|--no-non-production] [--disable-placement\|--enable-placement] [--builder\|--no-builder] [--base-domain D]` | Operator: what the server accepts, whether it builds, and the base domain of generated hostnames (`""` removes it) |
| `server revoke NAME`, `server rotate-key NAME`, `server remove NAME` | Operator: forget a server's key and drop its link (it must join again; its apps keep running); have an online server switch to a new key; remove a server no live project uses (archived projects' environments move to the control plane's server) |
| `update [--refresh]`, `update apply [VERSION] [--detach]` | Operator: the version the platform runs, the releases newer than it with their notes, and each server's version (the release check is reused for 15 minutes; `--refresh` checks now). `apply` updates the control plane (verified download, backup, swap, health check, rollback) and then every node behind it over its link, waiting until done unless `--detach` ([ADR 0028](decisions/0028-updates-from-the-console.md)) |
| `address`, `address set HOSTNAME`, `address remove` | Operator: Dock's public address, served over HTTPS by the control plane's Caddy, with its status (checked now: DNS, then the certificate) and the DNS record it needs, beside the private address, which always works. `set` moves it; `remove` stops serving it ([ADR 0029](decisions/0029-public-address.md)) |
| `registry`, `registry prune`, `registry migrate --to s3\|local [--storage NAME] [--yes]` | Operator: where the image registry keeps its images (this server's disk or a storage connection), their size, the last prune and migration (since the daemon started) and every service with images, with how many its rollback window keeps; `prune` deletes the images no release keeps and collects their layers now, in the background (409 while builds or pulls use the registry) ([ADR 0025](decisions/0025-platform-console.md)); `migrate` moves the images to a bucket or back in the background: read-only while copying, builds and pulls wait, then the registry switches and the old copy is deleted ([ADR 0034](decisions/0034-registry-object-storage.md)) |
| `storage`, `storage add NAME --endpoint URL --bucket B [--region R] [--path-style=false] --access-key-id ID --secret-access-key-stdin`, `storage set NAME [same flags]`, `storage rename NAME NEW`, `storage remove NAME [--yes]` | Operator: S3-compatible storage connections (AWS, Hetzner, R2, B2, MinIO, Garage), one bucket each, defined once by name. `rename` touches nothing else: what uses the connection follows. `add` and `set` list the bucket with the keys before saving; keys are never shown. A connection the registry uses can't be removed or moved to another endpoint or bucket; new keys restart the registry ([ADR 0034](decisions/0034-registry-object-storage.md)) |
| `cloudflare`, `cloudflare add NAME --token-stdin [--account-id ID]`, `cloudflare set NAME --token-stdin`, `cloudflare refresh NAME`, `cloudflare remove NAME [--yes]`, `cloudflare records`, `cloudflare retry` | Operator: Cloudflare connections, API tokens with which Dock runs tunnel servers' tunnels and routes domains added from their zones through them. `add` and `set` check the token and list its zones; tokens are never shown. `records` lists the domains Dock routes, failed ones first; `retry` tries failed ones again. A connection a tunnel uses can't be removed ([ADR 0036](decisions/0036-cloudflare-api.md)) |
| `cloudflare zones` | The zones domains can be added from (`domain add --cloudflare`); deploy scope |
| `server tunnel create\|adopt SERVER [--cloudflare NAME]` | Operator: have Dock run a tunnel server's Cloudflare tunnel: create one, or take over the one it runs (its routes stay). `server add --tunnel [--cloudflare NAME]` creates it once the server joins, and `domain add --cloudflare` does either when needed ([ADR 0036](decisions/0036-cloudflare-api.md)) |
| `domain add HOST --cloudflare` | Add a hostname from a zone connected to Dock (`cloudflare zones`): Dock routes it through the server's tunnel (creating or adopting it) and creates its proxied CNAME, deleted with the domain. The server must be in tunnel mode; a record Dock didn't make at that name is refused ([ADR 0036](decisions/0036-cloudflare-api.md)) |
| `analytics enable HOSTNAME [--site S]`, `analytics disable HOSTNAME`, `analytics sites`, `analytics site SITE`, `analytics site set SITE [--name N] [--exclude-path P]... [--ignore-ip IP]... [--replay on\|off] [--replay-sample N] [--event-retention-days N] [--public on\|off] [--public-domain HOST] [--public-errors on\|off] ... [--reset SETTING]...`, `analytics site public SITE`, `analytics stats SITE [--range 24h\|7d\|30d\|90d\|12m] [--filter DIM:OP:VALUE]...`, `analytics errors SITE [GROUP] [--range R] [--filter F]...`, `analytics vitals SITE [--by DIM] [--range R] [--filter F]...`, `analytics replays SITE [REPLAY] [--errors] [--range R] [--filter F]...`, `analytics goals SITE [--range R] [--filter F]...`, `analytics goals add\|remove SITE NAME [page:/path\|event:name]`, `analytics funnels SITE [FUNNEL] [--range R] [--filter F]...`, `analytics funnels add SITE NAME STEP STEP...`, `analytics funnels remove SITE NAME`, `analytics defaults [set ...]` | Dock Analytics' sites ([ADR 0037](decisions/0037-analytics.md)): `enable` makes a domain count visits (deploy): it joins `--site`, its service's other hostnames' site, or a new one named after it, and its pages need `<script src="/_dock/a/s.js" defer></script>`; `disable` stops it, the site keeping its history. `stats` is the dashboard as text. `errors` lists error groups (type, message, top frame), or with GROUP one group's stack, pages, browsers, releases and occurrences; `vitals` is LCP, INP, CLS, FCP and TTFB at p75, overall and `--by` page, device, browser, country, service or release. `replays` lists recorded visits (or one's page loads and timeline); they are watched in the UI, which audits every view. `goals` shows each goal's converted visitors and rate; `funnels` lists funnels or shows how many sessions took one's steps in order; adding and removing them needs deploy. `site set` changes a site's settings (deploy); `--public on` gives it a public, read-only dashboard at `stats-<site>.<base domain>` and `--public-domain` at a domain of its own; `site public` shows those addresses, checking their DNS and certificates now; `--reset` goes back to the install's default; `defaults` are those (operator). `domain add --analytics keep\|new` decides what a hostname with analytics history does on another service |
| `analytics`, `analytics server [SERVER] [--start-empty]` | Operator: Dock Analytics' state: whether it is on, the server the analytics store runs on, whether the store answers (asked now; one that doesn't is started again), a move running and the last problem. `server` runs the store on SERVER (none: the control plane's own) and turns analytics on; every server keeps its events in its spool until the store has them. A store on another server moves with its data, then is removed from there; `--start-empty` moves away from an offline server, whose store goes when it is back ([ADR 0037](decisions/0037-analytics.md)) |
| `token list [--mine]\|create NAME --scope [--operator]\|update ID\|revoke ID` | The organization's tokens and its members' browser-login tokens (admin). `list --mine` and `revoke` of one of those work with any role for your own tokens ([ADR 0038](decisions/0038-own-tokens.md)). `create --operator` (operator, scope admin) makes a token that may also run the platform. `create` and `update` take `--data-production`, `--data-non-production` (`none`, `schema`, `read`, `propose`, `write`) and `--data-sensitive` (`masked`, `visible`): the token's own database access instead of the data policy's tokens rule; `update --data-clear` removes it |
| `user list\|create [--operator]\|disable\|enable\|delete\|reset-password` | Users. `list` (admin) shows the organization's members with their role, status, live sessions and usable tokens; the rest manage accounts, which are the platform's (operator), and `create` adds the new user to the organization in use. Users are named by email or id; `reset-password` sets `--password` or prints a generated one once, and ends the user's sessions and browser-login tokens (your own: `me password`). `disable` and `delete` end them too; tokens the user created by hand stay |
| `audit [-p -e -s] [--action prefix] [--platform]` | The organization's audit log; the filters are explicit flags (the link isn't used), `-s` needs `-p` and `-e`. `--platform` (operator) shows the platform's instead: servers, accounts, logins, backup settings, alert channels |
| `serve`, `admin bootstrap\|caddy-config\|registry-config --out DIR\|registry-storage NAME --endpoint URL --bucket B\|firewall apply\|remove\|backup [-o FILE\|-] [--include-registry] [--move]\|restore ARCHIVE [--old-server-becomes-node] [--print-config]\|update` | Server side, run on the server with `--config`: the control plane daemon, the first operator token, Caddy's initial config (works before a node joins), the image registry's `config.yml` and `jwks.json` (deterministic for a given key and storage), a new registry's bucket (install.sh, keys on stdin; ADR 0034), the app firewall (root; installed as `dock-firewall.service`), and the control plane's state as one 0600 archive and back (ADR 0021; `--move` needs the daemon stopped and retires it, `restore` is run by `install.sh --restore`). `serve` exits 9 on a control plane that moved away. `admin update` is the updater, run as root by `dock-update.service` when the daemon asks for an update (ADR 0028) |
| `node`, `node join TOKEN\|--token-file FILE`, `node set-control-plane HOST:PORT` | On a server that joins a control plane, with `--config`: the node daemon (`dock-node.service`: the link, the registry proxy on `127.0.0.1:5000`, Caddy's `ask`; no API, UI or database), and joining once with a token from `server add` (writes `node.key` and `node.toml` into the data dir, prints the assigned server name). `--token-file -` reads the token from stdin, keeping it out of the process list. `install.sh --node` runs both. `node set-control-plane HOST:PORT` points a joined node at a control plane that moved; the running daemon follows within 30 s |

Commands that start releases (`deploy`, `promote`, `rollback`, `restart`, `apply`,
`--deploy`) follow them until they are live or failed; `--detach` returns
right after they are queued. Ctrl-C stops following, not the deploy.

### `dock db`

The database panel ([guide](guide/data.md), [ADR 0016](decisions/0016-database-panel.md)).
Every subcommand but `policy` acts on the linked service (or `-p -e -s`).
Access is the data access policy's decision; a permission error (exit 3)
says which level was missing and the rule that decided it.

| Command | What it does | Needs |
|---|---|---|
| `db status` | Engine, version, setup state, who uses it, your access and why | level schema |
| `db setup [--yes]` | Create or rotate Dock's roles (`dock_reader`, `dock_writer`) | admin |
| `db settings [--database postgres\|none\|detect] [--url-variable NAME]` | Mark a service as a database, or name its connection URL variable. Immediate, never a release | deploy |
| `db tables`, `db schema [TABLE]` (`describe`) | Tables; one table's (or every table's) columns, keys, indexes, constraints | level schema |
| `db rows TABLE [-w col=op:value ...] [--order col\|-col] [-n N] [--after CURSOR] [--count] [--reveal]` | One keyset page; prints the next cursor on stderr. Sensitive columns are masked; `--reveal` shows them if your access allows (audited). `--json` prints the page with `next` | level read |
| `db query SQL\|-f FILE\|- [-n N] [--timeout D] [--csv]` | One read-only statement, streamed: a table, CSV, or NDJSON frames with `--json`. Default 1 000 rows, at most 100 000; timeout at most 60s | level read |
| `db history [--before ID] [-n N]` | Your past queries here, newest first | level read |
| `db saved list\|save NAME SQL\|run NAME\|delete NAME` | Your saved queries; `save` replaces one with the same name, `run` takes `query`'s flags | level read |
| `db changes list [--status S]\|show ID\|commit ID [--confirm ENV]\|reject ID [--reason]` | Changesets. Committing someone else's approves it | admin to approve |
| `db apply -f FILE [--dry-run\|--propose] [-m MSG] [--confirm ENV]` (`propose`) | Changes from JSON: preview the SQL, or store and commit them in one transaction (level write), or leave them pending for an admin (level propose, or `--propose`) | level propose |
| `db policy show\|set [-p P [-e E]]` | The policy at the default, a project or an environment (explicit flags, not the link). `set deploy.production=read tokens.non-production=propose,visible ...` changes the named rules (`inherit` removes one); `--replace` drops the rest | admin to set |
| `db sensitive list\|set [schema.]table.column...\|--clear` | Columns masked for callers whose access says masked; `set` replaces the list | admin to set |

Filters (`-w`, `--filter`): `col=value` (equals), `col=op:value` with `eq`,
`ne`, `lt`, `lte`, `gt`, `gte`, `like`, `ilike`, `in` (comma-separated), or
`col=is_null` / `col=not_null`. Values are Postgres text input.

## CLI-only build

The release binary `dock` is the CLI, the daemon and the node runtime. A
laptop or CI runner only needs the client: `make build-cli` builds it with
the `dockcli` build tag, which leaves out `serve`, `node`, `admin` and `openapi` and
with them Docker, BuildKit, Caddy, SQLite and the API server.

```sh
make build-cli   # bin/dock-cli for this machine, plus bin/dock-cli-<os>-<arch>
                 # for darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64
```

| | `dock` | `dock-cli` |
|---|---|---|
| Size | 34 MB | 8.5 MB |
| `dock context list` | 5.1 ms, 28 MB RSS | 1.8 ms, 11 MB RSS |
| Package init | 2.9 ms | 0.3 ms |

Every other command behaves the same in both. Releases publish `dock-cli`
for these four platforms; see [install the CLI](guide/install.md#the-cli).
