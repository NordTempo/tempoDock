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
- `--json-file FILE` writes the `--json` value to FILE as well, while the
  output stays human and progress goes on: a CI log shows the build, and
  the job reads the result from the file. The file holds one compact JSON
  value per line: the result (what `--json` prints on stdout), then, if
  the command failed, the error object (what `--json` prints on stderr).
  A failed deploy writes both its release and the error; an error before
  any result writes only the error. The file is emptied when the command
  starts, and a path it can't write is a usage error before anything is
  done. Read it with `jq`, which takes a stream of values:
  `jq -r 'select(.error) | .error.code' FILE`.
- `dock run --json` sends the command's output to stderr and prints only
  the run on stdout once it ended ([`dock run`](#dock-run)).
- Progress (deploy steps, build output) goes to stderr and is silent with
  `--json`.
- With `--json`, an error is one JSON object on stderr:

  ```json
  {"error": {"code": "not_found", "exitCode": 4, "message": "context \"x\" not found", "hint": "Run `dock context list` to see contexts."}}
  ```

  `code` is the server's machine-readable reason when it gave one (e.g.
  `health_check_failed`), otherwise the exit code's name from the table below.
  A refused call (`forbidden`, exit code 3) says why in `message` and what
  would allow it in `hint`: the role, scope or limit to change, or the
  credential the platform needs, and the org's admins to ask when the
  caller is a person (ADR 0080).
- Human output uses aligned columns and plain status glyphs (`✓ ✗ ! ·`).
  Color only when the stream is a terminal and `NO_COLOR` is unset.
- The CLI only prompts when stdin is a terminal. Otherwise missing input is a
  usage error naming the flag to pass. Commands that ask for confirmation
  (removals, archives, discards, moves, drains, prunes, `db setup`; their
  `--help` lists `-y, --yes`) need `--yes`, commits to production data
  and `dock connect` to production need `--confirm <environment>`, and
  `dock project delete` needs `--confirm <project>`.

## Exit codes

Stable; never renumbered.

| Code | Name | Meaning |
|---|---|---|
| 0 | `ok` | Success |
| 1 | `error` | Anything not covered below |
| 2 | `usage` | Bad flags, arguments or input. Nothing was done |
| 3 | `auth` | No server configured, no token, or the token lacks permission, or the organization or platform requires two-factor sign-in the token's login didn't use (`two_factor_required`) or a step-up that must sign in through its identity provider again (`sso_required`) |
| 4 | `not_found` | The project, service, context, link, ... doesn't exist, or `promote` found nothing to promote |
| 5 | `conflict` | Already exists, another operation (e.g. a deploy) holds the lock, or the environment is locked (`environment_locked`) |
| 6 | `deploy_failed` | Build or health check failed. Traffic was not switched |
| 7 | `unreachable` | Could not reach the server (or its proxy answered 502/503/504), or the server an environment is placed on is offline (`server_offline`) |
| 8 | `incompatible` | CLI and server versions don't work together |
| 9 | `moved` | `dock serve` only: this control plane moved to another server (`dock admin backup --move`); systemd doesn't restart it |
| 10 | `deploy_canceled` | Someone canceled the deploy before it went live (`dock release cancel`). Traffic was not switched |

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
`dock context add NAME --server URL --token T [--use]`,
`dock context list|use|current|remove`. `dock login` creates one, and
`dock logout` removes
the token of the context in use (and revokes it, if `dock login` got it in
the browser).

The server and token come from, in order:

1. `DOCK_URL` and `DOCK_TOKEN` (and `DOCK_PLATFORM_TOKEN`, below).
   Contexts are ignored entirely, so no config file is needed (CI, agent
   sandboxes).
2. The context named by `DOCK_CONTEXT`.
3. The context of the linked directory (see below).
4. The current context.

A token only ever goes to the server it was given for: `DOCK_TOKEN` is
used only together with `DOCK_URL`. Setting `DOCK_TOKEN` alone is a usage
error (exit 2) rather than sending it to a context's server. The client
never follows redirects either; a 3xx is an error naming its target.
`dock context current` shows what the current directory resolves to and
why. Before its first change on a server that isn't on this machine (not
localhost or a loopback address), a command names it on stderr, as
`→ prod (https://dock.example.com)`; like other warnings, not with `--json`.

Log in with `dock login <server-url>` (no URL: the current context's
server). On a terminal it logs in through the browser, so there is no token
to create or paste:

1. The CLI prints a link and a code like `BCDF-GHJK` and opens the link
   (not over SSH or without a display; `--no-browser` only prints it, which
   also works without a terminal).
2. In the browser, sign in if needed (with your second factor, if you
   have one) and type the code from the terminal.
   The link never contains the code, so a link someone else sends you
   can't approve their login. The page then shows the CLI's host, OS and
   IP: deny a login you didn't start. A few wrong codes lock code entry
   for a few minutes.
3. The CLI gets its own token: named `cli: <hostname>`, acting with your
   role in each organization, valid for 90 days, listed on your account
   page (and the organization's Tokens settings) as from `dock login`.
   Codes expire after 10 minutes; Ctrl-C cancels.

With two-factor sign-in ([guide](guide/two-factor.md)), approving is a
sensitive action: a browser sign-in older than 15 minutes confirms with
your passkey or authenticator app first. The token counts as two-factor only when
the sign-in that approved it used a second factor; after adding your
first factor, run `dock login` again. `dock whoami` says which it is.

The token acts for you, so it is only as good as your account: it stops
working when you are disabled or deleted, never has more access than your
current role, and changing or resetting your password revokes it (except
the token that changed it with `dock me password`).

`dock logout` revokes a token from a browser login on the server, and
logging in again replaces (and revokes) it. A denied or expired login exits
3. The browser and the CLI may be on different machines: the CLI never
listens on a port, it waits on the server.

Running the platform (servers, accounts, updates, backups, the registry and
its settings) takes a platform credential, never an organization's
([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)).
`dock login --platform` makes a platform login for a person with a platform
role (operator or viewer): it acts in no organization, and the context keeps
it beside the org login. A command uses the org login; when the server
refuses a call because it runs the platform, the CLI sends it again with the
platform login, once. A platform token (`dock_p_...`, from `dock platform
token create`) given with `--token-stdin` is kept the same way, and
`DOCK_PLATFORM_TOKEN` sets one beside `DOCK_TOKEN`. `dock whoami` shows
both; `dock logout --platform` removes only the platform login. `dock login`
tokens from before this keep running the platform until they expire, and
the bootstrap token is both until an operator splits it (`dock platform
token split`).

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

An organization's old slug keeps naming it after an admin changes it
(`dock org rename --slug`), until another organization takes it.

`--reason <text>` works on every command: it says why you make a change,
and when a locked environment turns the change into an access request,
the admin deciding it sees the reason.

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
| `self-update [--latest \| --to vX.Y.Z] [--dry-run]` | Replace this CLI with the release's `dock-cli` for this machine, checked against its `SHA256SUMS` and that file's signature (a release from v0.56.0 on without a valid one is refused; an older, unsigned one installs with a warning, and only with `--to`): by default the version the current context's server runs (the latest release when no server answers, with a warning), `--latest` the newest release, `--to` a given one. A server's version or the latest release that is older than this CLI, or from before v0.56.0, is refused with the `--to` command that installs it anyway. The binary (symlinks followed) is swapped by a rename in its own directory; when that directory isn't writable the error names the root command, with `--to` pinned (root has no CLI context). Already on the target: says so, exit 0. `--json`: `from`, `to`, `target` (`server`, `latest` or `pinned`), `server`, `serverError`, `asset`, `path`, `updated`, `dryRun`, `unsigned` (the release predates signing). The CLI-only build only; the server build points to `dock update apply` |
| `logout` | Remove the context's token; revoke it on the server if it came from a browser login |
| `whoami` | Who the token belongs to, the organization it acts in and its scope there, and whether its login used a second factor (with a warning when an organization or the platform requires one it didn't) |
| `org list\|create <slug> [--name N]\|use <slug>` | Your organizations, your role in each and whether it requires two-factor sign-in; create one (users, not tokens) with you as its admin; act in one from now on, in the context in use |
| `org members`, `org member role <email\|id> <role>\|remove <email\|id> [-y]` | The organization's members (admin); change a role (it always keeps an admin); take someone out (their account stays) |
| `org invite <email> [--role R]`, `org invites [revoke <id>]`, `org join <link\|code>` | Invite an email address (admin): emails the link when Dock's email is set up (`dock email`) and prints it; it works once, for 7 days, only for that email; list or revoke open invites; accept one. Someone without an account opens the link in a browser and creates it there (ADR 0060) |
| `org env list [--reveal]\|set KEY=value...\|unset KEY...\|redeploy KEY` | Organization variables, used as `${{org.KEY}}` ([guide](guide/variables.md#organization-variables)): set and reveal (admin), remove one nothing uses, redeploy the running services that use one |
| `org two-factor [on\|off]` | Whether the organization requires its members to sign in with a second factor; `on` or `off` changes it (admin with access to every project; turning it on needs your own sign-in to have used one). Members without one stay signed in but can't act in it (`two_factor_required`, exit 3) |
| `org rename [<name>] [--slug S]`, `org leave [-y]`, `org delete [-y]` | Change the name, the slug (the address) or both (admin; ADR 0064): the old slug keeps working until another organization takes it, and this machine's contexts and links that named it follow; leave (the last admin can't); delete an organization without projects (admin) |
| `mcp [--show-token] [--name N]` | Print the MCP client config for the context's server ([agents](guide/agents.md#mcp)): the endpoint URL, a `claude mcp add` command and an `mcp.json` entry. The token is `${DOCK_TOKEN}` unless `--show-token` |
| `me name <name>` | Change your display name (1 to 64 characters, trimmed). Needs a browser-login token, which acts for you; API tokens have no account. The account page in the browser has the same |
| `me password [--password-stdin]` | Change your own password: prompts for the current one and the new one twice, or reads both from stdin (one per line). Needs a browser-login token, which acts for you; API tokens have no password. Your UI sessions and other browser-login tokens end; the CLI's own token stays valid |
| `me two-factor` | Your second factors, passkeys and authenticator apps (kind, name, added, last used), and how many unused recovery codes you have. Needs a browser-login token; setting factors up, removing them and new recovery codes are on your account page in the browser |
| `me email [access-requests on\|off]` | Which optional mail Dock sends you, or turn one on or off: access requests waiting for you to decide (admins). Invites, password reset links and security notices always come ([ADR 0079](decisions/0079-dock-sends-its-own-email.md)). Users only |
| `link [-p -e -s]`, `unlink` | Link the directory. Pickers on a terminal; otherwise `--project` (and `--environment` when there are several) |
| `deploy [dir] [--image ref] [-d]` | Pack and stream the directory (or deploy an image) and follow build, start, health check and cutover. A `--service` that doesn't exist yet is created. An image deploy sets the service's `image` setting; a source deploy clears it. Exit 6 when the release fails. When the environment's server is offline or revoked, a source deploy stops before packing anything (`server_offline`, exit 7) |
| `promote FROM TO [-s service...] [-d]` | Deploy the images serving in environment FROM to TO (same project), by digest, with TO's own variables and settings; nothing is rebuilt. One service at a time, stopping at the first failure. Services missing in TO (or with nothing running in FROM) are skipped and listed. Needs deploy on TO. Exit 6 when a release fails, exit 4 when nothing was promoted (every service skipped; the skipped list is still printed, and with `--json` the result still goes to stdout) |
| `status [-a]` | The service's state, releases, domains and staged changes; `-a` (or no service) for the whole environment. On a server that is offline or revoked the state is the last known one and says so ("server t3node is offline: last known running"; `server_offline`, `server` and `server_status` in the JSON status). A service with uptime checks shows their state and 24-hour uptime (`uptime` in the JSON) |
| `logs [-f] [-n N] [--since] [-r N] [--grep TEXT] [--stderr] [--build]` | Service output, or a build log. `-r N` reads an earlier release's output, kept on its server after its container was removed (`[logs]` limits). `--grep` keeps lines containing TEXT (plain; all lower case matches any case) and `--stderr` the lines written to stderr, filtered on the server after `--tail`: `-n 100 --grep x` prints the matches among the last 100 lines (`-n 0` searches all). With `-f`, new matching lines keep coming, and a follow of the active release goes on to the next one a deploy makes live; it says why it ends otherwise. Not with `--build` |
| `logs drain list`, `logs drain add http\|syslog\|loki URL [--name N] [--header K=V]... [--token T]`, `logs drain test\|remove ID\|NAME [-y]` | The environment's log drains ([guide](guide/deploying.md#log-drains)): every service's output sent to HTTP (JSON lines), syslog or Loki, with lines sent and dropped, the last error and last delivery. Adding, testing and removing need admin. `-` as the URL or `--token` reads it from stdin. `test` exits 7 when the line didn't go through |
| `errors [-p P [-e E [-s S]]] [--state open\|resolved\|all] [--limit N]`, `errors show ID`, `errors resolve ID` | The organization's error issues ([ADR 0062](decisions/0062-org-alerts-and-operators.md#one-place-for-errors)): errors grouped by fingerprint per service, from the containers' logs (a stated error level, a stack trace, a panic), the frontend errors Dock Analytics collects and minutes with many 5xx answers; new first, then regressed, then ongoing. `--service` takes the linked project and environment. `show` is an issue's counts, releases and latest sample (secret values masked), and while tracing is on the command that shows its latest trace; `resolve` (deploy) marks it resolved, and seen again it regresses and alerts. Issues resolve by themselves after 7 days without an error |
| `traffic [--range 24h\|7d\|30d]` | What the service's domains served: requests, errors, average time, bytes sent and the most requested paths, with IDs as `:id`, each with its share of 5xx answers and its p50, p95 and p99 times (from every request, [ADR 0075](decisions/0075-tracing-over-otlp.md)) ([ADR 0033](decisions/0033-traffic-statistics.md)). Visitors are in `dock analytics stats` |
| `traces [--range 1h\|24h\|7d] [--slowest] [--errors] [--route R] [--method M] [--release N] [--since-release N] [--slower-than D] [--limit N]`, `traces show TRACE [--at T]` | The service's traces ([ADR 0075](decisions/0075-tracing-over-otlp.md), [guide](guide/tracing.md)): each one's entry span with its route, status, duration and release, and why Dock kept it; `show` prints a trace as a tree of its spans across services, with offsets, durations and exceptions. Needs tracing on |
| `metrics [--range 1h\|24h\|7d\|30d]`, `metrics show METRIC [--agg A] [--by ATTR] [--range R]`, `metrics pin\|unpin METRIC [--agg A] [--by ATTR]` | The service's app metrics over OTLP ([ADR 0075](decisions/0075-tracing-over-otlp.md), [guide](guide/tracing.md#app-metrics)): its pinned charts (Dock's charts of the runtime metrics it sends until someone pins others) with each one's latest value and range, then every metric it sent, with the points its series caps cut and the attribute to drop. `show` reads one metric, a line per value of `--by`; `pin` and `unpin` change the service's charts (deploy scope). Needs tracing on |
| `uptime list\|add URL [--status 200\|2xx\|3xx] [--interval 1m] [--timeout 10s]\|domains\|update ID [--interval 5m] [--status 2xx]\|remove ID` | Uptime checks: a URL on one of the service's domains, probed with GET from another server (the control plane when there is none). `list` shows each check's state, uptime over 24 hours, 7 and 30 days, last probe and the probing server, `(auto)` after the URL of a check Dock added for a verified domain ([ADR 0059](decisions/0059-analytics-area-and-slimmer-service-page.md)); two failed probes in a row open an `uptime_down` alert. `domains` adds a check of `https://<domain>/` for each verified domain without one. `update` changes a check's interval or expected status in place, keeping its history. `add`, `domains`, `update` and `remove` need admin; at most 10 per service ([ADR 0055](decisions/0055-uptime-checks.md)) |
| `releases`, `release list\|show N [--log-lines N]` | Release history and one release with its events. For a failed release, also the last lines of its build log and of its container's output (default 40 of each, `--log-lines 0` for none) |
| `diagnose [-r N]` | Why a release failed, or why the live one crash looped or turned unhealthy ([ADR 0071](decisions/0071-deploy-diagnosis.md)): the likely cause with its fix and what was ruled out, then the evidence (health check, exit, what changed since the last good release, pre-deploy command, last lines). The service's newest diagnosis, or release N's; `--json` is the whole bundle. Exit 4 when there is none |
| `vulns [-r N] [--min high] [--fixable]`, `vulns --all [--production]` | The vulnerabilities in the service's live image (or release N's), grouped by what fixes them: rebuilding on a newer base image or upgrading the app's dependencies ([ADR 0074](decisions/0074-image-vulnerability-scanning.md)). `--all` lists every finding at or above `--min` in the organization's live releases, worst first, and the services not scanned yet. Exit 4 before the image is scanned |
| `vulns ignore ID --reason WHY [--package P] [--in service\|project\|org] [--days N\|--always]`, `vulns ignores`, `vulns unignore ID` | Ignore a vulnerability where it doesn't matter: the service (default; deploy), every service of the project (`--in project`; deploy) or the organization (`--in org`; admin), for `--days` (90; at most 365) or `--always` (admin). It leaves counts, the overview, Images and alerts, and stays listed as ignored. `ignores` lists every ignore with why and until when (expired ones for 30 days); `unignore` removes one
| `images [--production]` | Every image the organization's live releases run, one row per image, worst first: its base, its vulnerabilities and when it was scanned |
| `scan [-r N]` | Queue the service's live image (or release N's) for a scan against the current database (deploy). It waits like every scan until no build or deploy has run for a minute |
| `sbom [-r N] [--format syft-json\|cyclonedx-json\|spdx-json]` | Print the image's SBOM: as Syft made it, or as CycloneDX 1.6 or SPDX 2.3 JSON for tools that take those |
| `scanning [on\|off]`, `scanning import FILE` | Operators: whether image scanning is on, scans by state and each builder's vulnerability database; `import` uploads a Grype database archive for installs without internet access, which every builder imports at the next quiet minute (with `[scan] offline`, the only way builders get one) |
| `release wait [N] [--timeout D]` | Follow a release until it is live or failed, like `deploy` does (default: the newest release), with the same output and exit codes: 0 when live, 6 when failed, 10 when canceled. A release that is already done is printed right away. `--timeout` (or Ctrl-C) stops following with exit 1; the deploy continues |
| `release cancel [N]` | Cancel a deploy before traffic moves to it (default: the release deploying now; deploy): any step from queued to the health check ([ADR 0086](decisions/0086-cancel-a-deploy.md)). What it started is removed, the serving release keeps serving and its changes are staged again; it returns once the release is canceled. Exit 5 when nothing deploys or the switch-over already began (then roll back) |
| `rollback [N]`, `release rollback [N]` | New release from an earlier one; default the one that served before the active one. Says which release it copies and that release's image and config |
| `restart` | New release identical to the active one |
| `connect [service] [--port N] [--confirm ENV] [--show-url]` | Tunnel `127.0.0.1` (the service's port if free, else a free one) to the service's private port until Ctrl-C, for psql, TablePlus, redis-cli ([guide](guide/data.md#connecting-from-your-machine)). Needs data access level write with sensitive visible; production needs `--confirm`, and API tokens never get production. `--show-url` prints the connection URL with its password (audited). `--json` prints one object (`service`, `port`, `local`, `url`, ...) before serving. Exit 3 without access |
| `run [service] [-i] [-t] [-d] [--timeout D] [--volumes none\|read\|write] [--no-snapshot] -- COMMAND...` | Run a one-off command in a new container from the service's active release, streamed; exits with the command's exit code ([below](#dock-run)) |
| `runs [service]`, `runs show ID` | The service's latest one-off commands (the newest 50), newest first; one with the tail of its output (deploy scope) and, when it failed, its likely cause ([ADR 0071](decisions/0071-deploy-diagnosis.md)) |
| `job list [service]\|set NAME --schedule CRON [--timezone TZ] [--timeout D] [--overlap skip\|allow] [--disabled] -- COMMAND...\|remove NAME\|enable NAME\|disable NAME\|run NAME [-d]\|history NAME` | A service's scheduled jobs ([`dock job`](#dock-job)): list them with their next and latest run; create or replace one; remove it with its history; turn it on or off; run it now and wait (exits with its code); its latest 20 runs |
| `git connect OWNER/REPO [--mode app\|webhook] [--branch B] [--dir D] [--token-stdin\|--remove-token] [--status] [--rotate-secret]` | Deploy on every push to a GitHub branch ([guide](guide/deploying.md#deploy-on-git-push)). Through Dock's GitHub App when it is installed for the org (statuses on, no token), else, or with `--mode webhook`, with its own webhook: prints the webhook settings for GitHub and its secret, once (again only with `--rotate-secret`). Running it again changes the branch, directory, token, statuses or mode. The token is read from stdin only and never printed |
| `git status`, `git deploy [--ref R]`, `git disconnect [-y]` | The GitHub source: the App or the webhook URL, token set or not, queued push, latest delivery; queue the branch's head (or a SHA, branch or tag) now, even if live; remove the connection and its token |
| `github status`, `github app create [--owner ORG] [--public] [--open=false]\|secret\|remove [-y]`, `github assign ID ORG` | Operator: Dock's GitHub App ([ADR 0031](decisions/0031-github-app.md), [0066](decisions/0066-github-apps-for-many-orgs.md)). `app create` and `github install` print a link valid for an hour (on stderr with `--json`) and open it in a browser; GitHub sends the browser back to Dock, which checks with GitHub that the installer can access the installation. `--public` lets any GitHub account install the App. `app secret` reads the client secret of an App made before Dock kept it from stdin. `status` lists the App's installations with their org; `assign` lets an org deploy from one |
| `github install [ORG] [--open=false]`, `github installations`, `github unassign ID` | Admin: install the App for the organization (another organization: operator), list the organization's installations, stop deploying from one |
| `github repos` | The repositories the org can connect through the App, the newest push first, with the services that deploy from each (deploy scope) |
| `github branches OWNER/REPO`, `github paths OWNER/REPO [--branch B] [--all]` | A repository's branches (the default first); its directories that look like an app (a Dockerfile, package.json, go.mod...) with the services that build them, `--all` every directory (deploy scope) |
| `env list [--reveal]` | Shared and service variables |
| `env set K=V... \| unset K... \| import FILE [--shared] [--deploy]` | Stage variable changes; `--shared` for environment-level, `--deploy` to apply right away |
| `changes [--reveal]`, `apply` | Show or apply (and follow the resulting releases) the environment's staged changes |
| `apply FILE [--plan\|--dry-run] [--prune] [--deploy] [-e ENV]` | A project file ([ADR 0049](decisions/0049-apply-and-compose-import.md)): show its difference from the live state, or stage it in each of its environments (`--deploy` applies them too, follows their releases and exits 6 when one fails). Removals only with `--prune`; secrets are names, never values |
| `import compose [FILE] [-p NAME] [-o OUT] [--apply] [--yes]` | Convert a docker-compose.yml into a project file and show its plan, with a warning for everything that doesn't map exactly; `-o` writes the file, `--apply` creates the project (asks for secrets on a terminal) |
| `discard [KEY...] [-s service] [--shared]` | Drop staged changes: all in the environment; with `-s`, that service's own; with KEYs, those variables' (the service's, or shared with `--shared`) |
| `domain list [-w]\|add HOST [--redirect-to]\|generate\|check HOST\|remove HOST` | Domains; `list -w` adds the URL, last DNS check and what the hostname resolved to; `add` prints the DNS record and waits (`--timeout`, default 5m) for DNS and the certificate, then prints the URL. A custom hostname routes only once the TXT record `add` prints first (`_dock-challenge.HOST`) proves your organization controls it; none is needed under the server's base domain, from Cloudflare, or for an operator. `check` looks for it, DNS and the certificate at once ([ADR 0042](decisions/0042-domain-verification.md)) |
| `access`, `access public\|login`, `access password --username U [--password-stdin]` | Who can open the service's domains: anyone, visitors with a username and password (basic auth; prompted twice, or one line on stdin; empty keeps the current one), or members signed in to Dock who can read the service. Takes effect at once on every domain ([ADR 0030](decisions/0030-access-protection.md)) |
| `volume list\|add NAME PATH` | Volumes (staged) |
| `volume snapshot [--note N] [--detach]`, `volume snapshot list\|delete ID\|restore ID [--as NAME\|--in-place]` | Volume snapshots on the service's server ([ADR 0047](decisions/0047-volume-snapshots.md), [0061](decisions/0061-one-restore-model.md)): the service's containers pause while its volumes are copied, so the copy is of one moment. The newest few per service are kept. `restore` of a template database makes a new service (`--as`, default `<service>-restored`) and leaves the service alone; `--in-place` (the only way for other services) stops it, takes a safety snapshot and copies the snapshot in. Off the server is `dock backup` |
| `service list\|create\|show\|update\|archive` | Services; `update` stages only the settings passed (`--pre-deploy-command CMD` and `--pre-deploy-timeout 15m` set the command each new release runs before it gets traffic, e.g. migrations; `--pre-deploy-command ""` removes it, [ADR 0045](decisions/0045-pre-deploy-command.md); `--pre-deploy-snapshot` snapshots the environment's stateful services before it, `=false` turns that off, [ADR 0047](decisions/0047-volume-snapshots.md)). `create NAME --template postgres\|pgvector\|mysql\|mariadb\|redis\|valkey` creates a database from a template and deploys it ([guide](guide/databases.md)); `create NAME --repo OWNER/REPO [--branch B] [--dir D]` connects it to a repository through the org's GitHub App (the branch and directory are checked there) and deploys the branch's head ([guide](guide/deploying.md#a-new-service-from-a-repository)) |
| `template list [--category C]`, `template show NAME`, `template use NAME [--name N] [--domain HOST] [--set KEY=VALUE]... [--plan\|--file] [-d]` | App templates ([ADR 0083](decisions/0083-app-templates.md), [guide](guide/templates.md)): an app's services (Umami with its Postgres, n8n, Forgejo, ...) and the databases. `use` (deploy scope) creates the services in the environment and deploys them: the main one `--name` (default the template's name), the others NAME-<part>; with a domain, `--domain` or a generated one; `--set` fills its inputs (`show` lists them). It waits for the first deploys unless `-d`, and exits 6 when one fails. Other staged changes stay staged. `--plan` says what it would create; `--file` prints it as a project file to edit and `dock apply`. The services are ordinary afterwards and remember the template (`service show`). `template save NAME [--services a,b] [--main S] [--title T]` (admin) saves the environment's services as the organization's template (variables that aren't references become secret inputs; values are never copied), `template save --file F` saves a template file, `template show NAME --toml` prints one, `template remove NAME` removes the organization's |
| `service switch-references [name] --to SERVICE [--dry-run]` | Rewrite every `${{name.KEY}}` reference in the environment's variables to `${{SERVICE.KEY}}` and apply it, so the services using `name` get config releases: how a restored database takes over (`dock service switch-references db --to db-restored`), and switching back is the same command the other way. `SERVICE` must be live and have every referenced key; refused while the environment has other staged changes. `--dry-run` lists the services that would change. Deploy scope ([ADR 0061](decisions/0061-one-restore-model.md)) |
| `service stop [name] [--yes]`, `service start [name] [-d]` | Stop a service (deploy scope): its domains answer 503 and its container is removed; its active release, port, volumes, domains and variables are kept. `show` and `status` say who stopped it and when. `start` is a release identical to the active one through the health-checked path, followed like `restart`; if it fails (exit 6) the service stays stopped. Any deploy also starts it again ([ADR 0050](decisions/0050-stop-start-service.md)) |
| `backup list\|log <id>\|run [-d] [--control-plane]\|restore --snapshot ID [--as NAME] [-d]\|schedule [--hour H\|--default-hour] [--on\|--off]` | A stateful service's backups, which run on their own: list runs and snapshots, print a run's log (the job's last 16 KB, secrets masked; the newest 20 runs keep one; deploy access), back up now (waits; `--detach` doesn't), restore into a new service (template databases), change the daily schedule (operator). `run --control-plane` (operator) backs up the control plane itself into the repository, as it does daily. Exit 1 when the awaited run fails |
| `backup status [--reveal]`, `backup configure [--keep-daily N] [--keep-weekly N] [--keep-monthly N] [--hour H]`, `backup move --storage NAME [--path P] [--yes]` | Where backups go, the repositories holding them and the latest move, retention and the default hour, for the whole platform; `configure` (operator) sets retention and the hour and prints the repository password the first time (buckets are storage connections: the old hidden `--repository` and key flags are refused); `--reveal` (operator) shows the password again; `move` (operator) moves every backup into a folder of a storage connection's bucket, keeps them restorable and empties where they were ([ADR 0039](decisions/0039-backups-on-storage-connections.md)) |
| `project list [--all]\|create\|archive <name>\|delete <name> [--confirm <name>]`, `environment list\|create [--kind]` | Projects and environments (`env` is variables). `project archive` (admin) archives every service in the project, removes its environments' networks and hides it from `project list` (`--all` shows it). `project delete` (admin, unlimited) deletes an archived project for good: its containers, networks, volumes and snapshots on its servers, its analytics events and everything Dock knows of it, which frees its name and domains; its activity and its backups stay, and the answer says where the backups are. Every server it is on must be online; the name is typed again, or passed with `--confirm` ([ADR 0078](decisions/0078-deleting-archived-projects.md)). An environment's kind (production or non-production) defaults from its name and never changes |
| `request list [--status S]\|show ID\|wait ID [--timeout 30m]` | Access requests: changes a locked environment refused and filed for an admin to approve in the UI. Admins see their organization's, a token its own. `wait` exits 0 when it ran, 1 when it failed, 3 when rejected, 4 when expired, 5 when the timeout passed first. There is no approve: tokens never decide ([ADR 0044](decisions/0044-environment-lock.md)) |
| `org locks [--lock-production on\|off] [--request-ttl 24h]` | Whether new production environments start locked, and how long access requests wait (unlimited admin to change) |
| `alerts [--all] [--limit N] [--platform]`, `alerts resolve ID`, `alerts vulns [critical\|high\|off]`, `alerts channel list\|add NAME (--webhook URL\|--slack URL\|--email TO,...) [--min-severity] [--renotify D]\|test NAME\|remove NAME` | The organization's open alerts, critical first (failed deploys, crash loops, unhealthy services, failed backups and jobs, uptime, domains, analytics); `--all` adds resolved ones. Alerts open and resolve by themselves; `resolve` (deploy) closes one whose condition won't clear. `alerts vulns [critical\|high\|off]` shows or (admin) sets which new vulnerabilities in live images alert (critical by default; [ADR 0074](decisions/0074-image-vulnerability-scanning.md)). Channels (admin) are where they are sent: signed webhooks, Slack, or email through Dock's email server (`dock email`); `-` reads a URL from stdin, and a webhook's signing secret is printed once. `--platform` (operator) works on the platform's alerts and channels instead: disks, memory, CPU, offline servers, updates, the control plane's backup ([ADR 0062](decisions/0062-org-alerts-and-operators.md)) |
| `status-page list\|show PAGE\|create NAME [--title T]\|update PAGE [--title T] [--show-uptime=false] [--drafts=false]\|rename PAGE NEW\|publish PAGE\|unpublish PAGE\|remove PAGE`, `status-page hostname PAGE [HOSTNAME] [--check] [--remove]`, `status-page hook list PAGE\|add PAGE NAME (--slack\|--discord\|--webhook) URL [--secret S]\|remove PAGE NAME\|test PAGE NAME`, `status-page component add PAGE NAME (--service P/E/S\|--check ID)... [--group G] [--description D] [--manual]\|remove PAGE NAME\|move PAGE NAME --to N` | Public status pages of the organization ([ADR 0073](decisions/0073-public-status-pages.md)): components with public names built from services and uptime checks, served at `status-<name>.<base domain>` once published. `show` gives each component's status now and 90-day uptime. `rename` moves the page's address; the old one redirects until another page takes the name. `component add` replaces a component of the same name. Everything but `list` and `show` needs admin with an unlimited token; `update --drafts=false` stops alerts drafting incidents; `hostname` gives the page a custom hostname, served once its TXT record proves the organization controls it (`--check` looks now), or at once when Dock makes its record in a connected Cloudflare zone; `hook` posts every public update of the page's incidents and maintenance to Slack, Discord or a signed webhook (`-` reads the URL from stdin; only its host is shown again) |
| `incident list [PAGE]\|show ID`, `incident open PAGE TITLE [--component NAME:degraded\|partial\|major]... [-m TEXT] [--state S] [--draft]`, `incident update ID -m TEXT [--state investigating\|identified\|monitoring\|resolved] [--component ...]`, `incident publish ID [-m TEXT]`, `incident resolve ID [-m TEXT]`, `incident edit ID [--title T] [--component ...] [--draft-message TEXT] [--update UPDATE -m TEXT]`, `incident remove ID [-y]` | Incidents on status pages ([ADR 0073](decisions/0073-public-status-pages.md)): what is posted is public at once; `--draft` keeps it in Dock until `publish`. While open, each `--component` reads at least its impact. `update` keeps the incident's state unless `--state` says otherwise; `--component` replaces its impact. `edit --update` fixes a posted update's words (the page says "edited"). Deploy scope; a token limited to a project names only components built from it. `remove` needs admin |
| `maintenance schedule PAGE TITLE --component NAME... (--to TIME\|--for DURATION) [--from TIME] [-m TEXT]`, `maintenance start\|complete\|cancel ID [-m TEXT]`, `maintenance extend ID (--to TIME\|--by DURATION)` | Maintenance windows on status pages ([ADR 0073](decisions/0073-public-status-pages.md)), public at once. Dock starts a window at `--from` (default now) and completes it at its end; while it runs its components read maintenance, alerts about their services are held until it ends and their uptime isn't counted. Times are UTC unless they carry an offset (`2026-11-13T02:00Z`). `dock incident list` and `show` list and show windows too. Deploy scope |
| `environment lock [ENV] [--reason R] [--people]`, `environment unlock [ENV] [--for 30m]` (also `env lock\|unlock`) | Lock an environment (admin): tokens, this CLI included, can still read it but every change they try fails with `environment_locked` (exit 5) and files an access request (`dock request`); `dock deploy` uploads anyway and its archive waits for approval, a newer upload replacing it. People in the web UI are not affected, unless `--people`: then only its approvers (who hold approve there) change it, and everyone else's change waits for them too ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)). `unlock` (an approver there) unlocks, or with `--for` opens it for a while; run with a token in a locked environment it files an access request like any other change. `environment list` and `status` show the lock ([ADR 0044](decisions/0044-environment-lock.md)) |
| `environment sleep [ENV]`, `environment wake [ENV]`, `environment sleep-after [ENV] DURATION\|off` (also `env sleep\|wake\|sleep-after`) | Sleeping environments ([ADR 0070](decisions/0070-sleeping-environments.md)): `sleep` (deploy) stops every service of a non-production environment, keeping its containers; the next request to its domains, a deploy, a `dock run` or the Data panel wakes it. `wake` (read) starts them again, databases first, and returns once each passed its health check. `sleep-after` (admin) makes it sleep on its own after that long without requests (5m to 24h, or plain minutes like `45`; previews take `env previews set --sleep-after`, default 30m). Production never sleeps. `environment list` and `status` show the state (`asleep 2h (386 MB freed)`, `awake, sleeps in 12 min`); `--json` has `Environment.sleep` |
| `environment move [ENV] --to SERVER [--max-hold S] [-d] [--yes]`, `environment move [ENV] --status` (also `env move`) | Operator: move an environment to another server ([ADRs 0053](decisions/0053-moving-environments.md), [0063](decisions/0063-moves-without-downtime-and-server-drains.md)). Refused with the reason before anything changes unless the server accepts the kind, both servers are online and the target has the memory, disk, images and Cloudflare tunnels. Volumes copy in passes while their services serve; then requests are held for a few seconds (at most `--max-hold`, default 30) while the last changes copy and the target starts, and a move that wouldn't fit fails before anything stops. A failure before the switch rolls back. Waits, printing each step and each pass (`sync: db pass 2 took 1.2s, 812.0 MB so far`), then the hold and what is left to do (DNS records, old volumes); exit 1 if it failed. `--detach` returns once it started; `--status` shows the running or latest move (exit 4 if it never moved). The environment's lock applies |
| `environment clone FROM TO [--kind] [--deploy] [--copy-values] [-d]` (also `env clone`) | A new environment with FROM's services, settings, variables and new, empty volumes, placed like `environment create`. Reference-only values are always copied; other values only for admins, and between production and non-production only with `--copy-values` (admin, listed in the output); otherwise templates get new credentials and the rest are listed. Domains, git connections, releases, backups and staged changes are not copied. `--deploy` deploys FROM's images like `promote` ([ADR 0022](decisions/0022-clone-and-export.md)) |
| `project rename <current> <new>` | Rename a project and its address, archived ones too, which frees their name (admin with whole-project access). IDs, running services, domains, volumes and release history stay unchanged. Old project URLs and explicit names in scripts need updating; linked directories follow the project |
| `project export [NAME] [--format toml\|json]` (also `export`) | The project's live config: environments, services, settings, volumes, domains, git connections and variables (secrets by name, never values). Sorted, so it diffs well in git; `dock apply` reads it back ([project files](project-file.md)) |
| `environment previews`, `environment previews set ENV [--max N] [--sleep-after 30m\|off] [--copy KEY]...\|close ENV` (also `env previews`) | Preview environments per pull request ([ADR 0056](decisions/0056-preview-environments.md), [0077](decisions/0077-previews-belong-to-services.md), [guide](guide/previews.md)): which services build pull requests, each base's numbers and the open previews. `set` (admin on the environment) replaces the numbers of the previews cloned from ENV: `--max` open at once (default 3, at most 20), `--sleep-after` without requests (default 30m, `off` never; [ADR 0070](decisions/0070-sleeping-environments.md)), and the shared variables whose literal values they copy. `close` archives one now and frees its containers, volumes and addresses |
| `service previews [on\|off] [--forks] [--copy KEY]... [--seed]` | What a service does in previews (admin on its environment): `on`, pull requests into its branch that change its directory get a preview `pr-<number>` cloned from its environment, where it builds their code; `--forks` also from forks; `--copy` names its variables whose literal values previews copy (replaces the list); `--seed` fills a new preview's volumes from its newest snapshot. Without arguments it shows them; what isn't passed stays |
| `project pin <project> --kind production\|non-production --server NAME` | Operator: choose the server a project's environments of a kind go to, before any exist |
| `server list`, `server show NAME` | Servers: link status (`online`, `offline`, `incompatible`, `joining`, `revoked`), version, ingress, accepted kinds, whether it builds, memory reserved; operators only: servers are the platform's ([ADR 0062](decisions/0062-org-alerts-and-operators.md#servers-are-the-operators-2026-11-08)), and others see their environment's server status in `status`. `show` adds base domain, DNS target, what's placed and current usage |
| `server disk NAME`, `server prune NAME [--yes]` | Operator: where a server's disk goes (services' volumes, images by what keeps them, BuildKit's cache, Dock's own, the rest) and what a prune frees, read on demand (seconds); `prune` removes images no release needs and build cache above `build_cache_keep_mb` now ([ADR 0035](decisions/0035-server-disk-breakdown.md)) |
| `server add NAME [--production] [--non-production] [--builder]` | Operator: create a one-time join token (valid for an hour) and print the exact `install.sh --node` command to run on the new server. The token is shown once |
| `server update NAME [--production\|--no-production] [--non-production\|--no-non-production] [--disable-placement\|--enable-placement] [--builder\|--no-builder] [--base-domain D]` | Operator: what the server accepts, whether it builds, and the base domain of generated hostnames (`""` removes it) |
| `server rename NAME NEW` | Operator: rename a server; its environments, link and history stay |
| `server revoke NAME`, `server rotate-key NAME`, `server remove NAME` | Operator: forget a server's key and drop its link (it must join again; its apps keep running); have an online server switch to a new key; remove a server no live project uses (archived projects' environments move to the control plane's server; its forwards, drains and move history go with it) |
| `update [--refresh]`, `update apply [VERSION] [--detach]` | Operator: the version the platform runs, the releases newer than it with their notes, and each server's version (the release check is reused for 15 minutes; `--refresh` checks now). `apply` updates the control plane (verified download, backup, swap, health check, rollback) and then every node behind it over its link, waiting until done unless `--detach` ([ADR 0028](decisions/0028-updates-from-the-console.md)) |
| `address`, `address set HOSTNAME`, `address remove` | Operator: Dock's public address, served over HTTPS by the control plane's Caddy, with its status (checked now: DNS, then the certificate) and the DNS record it needs, beside the private address, which always works. `set` moves it; `remove` stops serving it ([ADR 0029](decisions/0029-public-address.md)) |
| `email`, `email set --host H --from ADDR [--port N] [--security starttls\|tls] [--username U --password-stdin] [--from-name N] [--reply-to ADDR]`, `email test [--to ADDR]`, `email log [--limit N]`, `email off` | Operator: the SMTP server Dock sends invites and other mail through ([ADR 0079](decisions/0079-dock-sends-its-own-email.md)), with how sending goes. `set` keeps flags left out and reads the password from stdin; setting and `off` need a recent sign-in in the browser. `test` sends one now and exits 1 if it fails. Without a server, invites print their link only |
| `security`, `security operators-two-factor on\|off` | Operator: the platform's sign-in policy. While on, an operator whose login used no second factor keeps their organization roles but can't run the platform until they add one; turning it on needs your own sign-in to have used one. Operator API tokens are not affected |
| `sso list\|add <name>\|set <name>\|remove <name>` | Operator, recently confirmed: the platform's identity providers (ADR 0065), one "Continue with" button each on the sign-in page. `add` takes `--client-id ID --client-secret-stdin`, and for a name other than google or github `--kind google\|github\|oidc`; oidc also `--issuer URL --label L`. `set` changes `--label`, `--issuer`, `--client-id` or the secret. `list` shows the redirect URL to register at every provider, and the switches below. Client secrets are never shown |
| `sso password-sign-in [on\|off]`, `sso invite-only [on\|off]` | Operator: whether the sign-in page takes passwords (off needs a provider and your own sign-in through one), and whether a provider creates accounts only for emails with an open invite (on by default; off: for anyone it signs in, in no organization). `org sso` and `org sso-only` are gone and say so |
| `registry`, `registry prune`, `registry migrate --to s3\|local [--storage NAME] [--yes]` | Operator: where the image registry keeps its images (this server's disk or a storage connection), their size, the last prune and migration (since the daemon started) and every service with images, with how many its rollback window keeps; `prune` deletes the images no release keeps and collects their layers now, in the background (409 while builds or pulls use the registry) ([ADR 0025](decisions/0025-platform-console.md)); `migrate` moves the images to a bucket or back in the background: read-only while copying, builds and pulls wait, then the registry switches and the old copy is deleted ([ADR 0034](decisions/0034-registry-object-storage.md)) |
| `registry login HOST --username NAME [--password-stdin]`, `registry logout HOST`, `registry credentials` | Admin: the organization's credentials for private image registries ([ADR 0051](decisions/0051-registry-credentials.md)). Deploys of images from HOST (`ghcr.io`, `registry.example.com:5000`, `docker.io` for Docker Hub and images named without a host) pull with them from the next pull on; other hosts are pulled anonymously. `login` adds or replaces them, reading the password or token from stdin with `--password-stdin` (all of it but a final line break) or asking on a terminal, never from an argument; it is stored encrypted and never shown. `credentials` lists hosts, usernames and when they were set. Dock's own registry needs none and is refused. Builds don't use them |
| `storage`, `storage add NAME --endpoint URL --bucket B [--region R] [--path-style=false] --access-key-id ID --secret-access-key-stdin`, `storage set NAME [same flags]`, `storage rename NAME NEW`, `storage remove NAME [--yes]` | Operator: S3-compatible storage connections (AWS, Hetzner, R2, B2, MinIO, Garage), one bucket each, defined once by name. `rename` touches nothing else: what uses the connection follows. `add` and `set` list the bucket with the keys before saving; keys are never shown. A connection the registry uses can't be removed or moved to another endpoint or bucket; new keys restart the registry ([ADR 0034](decisions/0034-registry-object-storage.md)) |
| `cloudflare`, `cloudflare add NAME --token-stdin [--account-id ID] [--platform [--shared]]`, `cloudflare set NAME --token-stdin`, `cloudflare refresh NAME`, `cloudflare remove NAME [--yes]`, `cloudflare records`, `cloudflare retry` | Admin: the organization's Cloudflare connections, API tokens with which Dock routes domains added from their zones through the servers' tunnels; only the organization's domains use them. With `--platform` (operator): the platform's connections, which run the servers' tunnels, and with `--shared` open their zones to every organization. `add` and `set` check the token and list its zones; tokens are never shown. `records` lists the domains Dock routes, failed ones first; `retry` tries failed ones again. A zone may be on several connections, the platform's and orgs' own; `set` and `refresh` report the zones added and dropped, and a dropped zone's domains move to another connection that has it. A connection a tunnel uses can't be removed; give it a narrower token with `set` instead ([ADRs 0036](decisions/0036-cloudflare-api.md), [0062](decisions/0062-org-alerts-and-operators.md)) |
| `cloudflare zones` | The zones domains can be added from (`domain add --cloudflare`); deploy scope |
| `cloudflare redirects`, `cloudflare redirects add HOST URL [--code 302] [--keep-path] [--connection NAME]`, `cloudflare redirects set HOST [--to URL] [--code N] [--keep-path]`, `cloudflare redirects remove HOST [--yes]`, `cloudflare redirects retry HOST` | Admin (`--platform`: operator): hostnames in the level's own Cloudflare zones that Cloudflare redirects to a URL at its edge. Dock makes a proxied placeholder record and a Single Redirect rule (the token needs Zone › Dynamic URL Redirects › Edit). A platform zone shared with organizations keeps its redirects on the platform. `cloudflare remove NAME --force` removes a connection with its redirects ([ADR 0068](decisions/0068-redirects-on-cloudflare-zones.md)) |
| `server drain SERVER [--kind KIND] [--to PROJECT=SERVER]... [--max-hold S] [--dry-run] [-d] [--yes]`, `server drain SERVER --status\|--resume [--skip]\|--cancel` | Operator: move every environment on a server, or every one of a kind, to other servers one at a time, without downtime ([ADR 0063](decisions/0063-moves-without-downtime-and-server-drains.md)); the server stops taking new environments (of the kind) at once. Shows the plan (each environment, its target from placement or `--to`, its data and problems) and asks; a plan with problems doesn't start, `--dry-run` only shows it. Follows each move until the drain ends; exit 1 if it paused on a move that failed (its environment stays where it was): `--resume` tries it again, `--resume --skip` leaves it. `--status` shows the drain and the ports the server forwards for services that moved away; `--cancel` stops after the move that runs |
| `server tunnel create\|adopt SERVER [--cloudflare NAME]` | Operator: have Dock run a tunnel server's Cloudflare tunnel: create one, or take over the one it runs (its routes stay). `server add --tunnel [--cloudflare NAME]` creates it once the server joins, and `domain add --cloudflare` does either when needed ([ADR 0036](decisions/0036-cloudflare-api.md)) |
| `domain add HOST --cloudflare` | Add a hostname from a zone connected to Dock (`cloudflare zones`): Dock routes it through the server's tunnel (creating or adopting it) and creates its proxied CNAME, deleted with the domain. The server must be in tunnel mode; a record Dock didn't make at that name is refused ([ADR 0036](decisions/0036-cloudflare-api.md)) |
| `analytics enable HOSTNAME [--site S]`, `analytics disable HOSTNAME`, `analytics sites`, `analytics site SITE`, `analytics site set SITE [--name N] [--exclude-path P]... [--ignore-ip IP]... [--replay on\|off] [--replay-sample N] [--event-retention-days N] [--public on\|off] [--public-domain HOST] [--public-errors on\|off] ... [--reset SETTING]...`, `analytics site public SITE`, `analytics stats SITE [--range 24h\|7d\|30d\|90d\|12m] [--filter DIM:OP:VALUE]...`, `analytics check SITE`, `analytics errors SITE [GROUP] [--range R] [--filter F]...`, `analytics vitals SITE [--by DIM] [--range R] [--filter F]...`, `analytics replays SITE [REPLAY] [--errors] [--range R] [--filter F]...`, `analytics goals SITE [--range R] [--filter F]...`, `analytics goals add\|remove SITE NAME [page:/path\|event:name]`, `analytics funnels SITE [FUNNEL] [--range R] [--filter F]...`, `analytics funnels add SITE NAME STEP STEP...`, `analytics funnels remove SITE NAME`, `analytics defaults [set ...]` | Dock Analytics' sites ([ADR 0037](decisions/0037-analytics.md)): `enable` makes a domain count visits (deploy): it joins `--site`, its service's other hostnames' site, or a new one named after the service's custom domain or it (renamed after another of its hostnames when that one goes away, unless you named it), and its pages need `<script src="/_dock/a/s.js" defer></script>`; `disable` stops it, the site keeping its history. `stats` is the dashboard as text. `check` says whether the site's tracking script works: active (an event in 24 hours), waiting, quiet or stopped, and what a check of its pages found wrong (no script tag, a host without analytics or a proxy that hides `/_dock/a/`, a Content-Security-Policy that blocks it), each with its fix; Dock fetches each hostname's page and `/_dock/a/s.js`, at most once a minute per site. `errors` lists error groups (type, message, top frame), or with GROUP one group's stack, pages, browsers, releases and occurrences; `vitals` is LCP, INP, CLS, FCP and TTFB at p75, overall and `--by` page, device, browser, country, service or release. `replays` lists recorded visits (or one's page loads and timeline); they are watched in the UI, which audits every view. `goals` shows each goal's converted visitors and rate; `funnels` lists funnels or shows how many sessions took one's steps in order; adding and removing them needs deploy. `site set` changes a site's settings (deploy); `--public on` gives it a public, read-only dashboard at `stats-<site>-<random>.<base domain>` and `--public-domain` at a domain of its own; `site public` shows those addresses, checking their DNS and certificates now; `--reset` goes back to the install's default; `defaults` are those (operator). `domain add --analytics keep\|new` decides what a hostname with analytics history does on another service |
| `analytics`, `analytics server [SERVER] [--start-empty]` | Operator: Dock Analytics' state: whether it is on, the server the analytics store runs on, whether the store answers (asked now; one that doesn't is started again), a move running and the last problem. `server` runs the store on SERVER (none: the control plane's own) and turns analytics on; every server keeps its events in its spool until the store has them. A store on another server moves with its data, then is removed from there; `--start-empty` moves away from an offline server, whose store goes when it is back ([ADR 0037](decisions/0037-analytics.md)) |
| `tracing`, `tracing on`, `tracing off [--yes]`, `tracing rotate-key [--yes]` | Operator: tracing over OTLP. `on` starts the trace store, which keeps spans in a container of its own (`dock-traces`) beside the analytics store, on the analytics server, for `[traces] retention_days` (7); it doesn't turn analytics on. Services' next deploys start with the OpenTelemetry variables ([variables](guide/variables.md#opentelemetry)), so apps with a stock SDK send spans with no setup. When `analytics server` changes the analytics server, the trace store starts empty there and the old one goes. `off` removes the trace store with every span it holds, and needs the analytics server online. `rotate-key` revokes every trace ingest token (a leaked one): traced services get new ones with their next deploy or restart, and their spans are refused until then |
| `token list [--mine]\|create NAME --scope\|update ID\|revoke ID` | The organization's tokens and its members' browser-login tokens (admin). `list --mine` and `revoke` of one of those work with any role for your own tokens ([ADR 0038](decisions/0038-own-tokens.md)). An org's token never runs the platform: that is a platform token (`platform token`). `create` and `update` take `--data-production`, `--data-non-production` (`none`, `schema`, `read`, `propose`, `write`) and `--data-sensitive` (`masked`, `visible`): the token's own database access instead of the data policy's tokens rule; `update --data-clear` removes it |
| `token access NAME\|grant NAME ROLE [-p P [-e E]] [--kinds K] [--for 4h]\|bypass NAME on\|off` | An organization's token is an org agent ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)): its scope and limit are grants (its scope's roles at its limit's place). `access` shows its grants and groups; `grant` gives it one more; `group add G NAME --agent` puts it in a group; `grant revoke ID` ends one; `bypass` exempts it from the agent rules. Needs agents.manage |
| `group list\|show G\|create NAME\|rename G NAME\|delete G\|add G EMAIL\|remove G EMAIL\|grant G ROLE [-p P [-e E]] [--kinds K] [--for 4h]\|revoke GRANT-ID` | Groups ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)): job functions that give their members built-in roles (`role list`) on the organization, a project or one environment, optionally for some environment kinds (production, other, previews) or a while. Every organization starts with Admins, Developers, Viewers and Approvers, and a member's role (`org role`) is their place in the first three. Needs members.read to list, members.manage to change, with a recent sign-in in the browser; the organization always keeps someone with Full access |
| `grant EMAIL ROLE [-p P [-e E]] [--kinds K] [--for 4h]`, `grant list`, `grant revoke GRANT-ID` | One member's own grant, not through a group: a one-off |
| `can [EMAIL]` | Your effective access (a member's, with members.read): each area role's level on the organization and in each environment, from groups and grants |
| `role list [--own]\|show NAME\|create NAME [--include R,..] [--permission P,..] [--description D]\|edit NAME [--name N] [--include ..] [--permission ..] [--description D]\|delete NAME` | The built-in roles: every area role (deploy, variables, logs, data, domains, analytics, status_pages, alerts, projects, org_settings, members, usage, activity), its levels and their permissions, plus approver and full. With `--own`, and `show` to `delete`: the organization's own roles, built-in roles and permissions under one name, granted by name like a built-in role; `edit` replaces only what its flags name; a role a grant names can't be removed. Needs members.read to read, roles.manage to change |
| `agent settings [--agent ID]\|set AREA=LEVEL... [--kinds K] [--ask\|--no-ask] [--agent ID]` | What your agents (your `dock login` tokens) may use of your access in the organization ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)): each area role's level per environment kind (a level, `all` or `off`), and where their changes ask you first. `--agent` narrows one agent. An agent may tighten this; loosening needs you signed in on the Account page |
| `agent rules\|rule AREA=LEVEL... [--kinds K] [--ask\|--no-ask]` | The organization's agent rules: the most every agent (people's `dock login` and its tokens) may use of an area per environment kind (`all` is no rule), and where their changes ask an approver first. Needs members.read to read, agents.manage to change. `token bypass NAME on\|off` exempts one token |
| `platform access-diff` | Where the permission model of [ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md) disagreed with today's checks since the daemon started, while it only watches (platform.view). Empty means it agreed with every check; a data policy rule that gives less than a wider one shows as a lowering rule |
| `platform access-model [shadow\|enforce]` | Show or switch whether groups and grants decide people's checks ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)). shadow, the default: roles decide and the model watches, counting disagreements for `platform access-diff`; enforce: groups and grants decide, from each person's next request. Switching needs platform.settings |
| `platform token list\|create NAME --role operator\|viewer [--expires 90d]\|revoke ID\|split ID` | The platform's tokens ([ADR 0080](decisions/0080-permissions-roles-groups-and-agents.md)): they run the platform with a platform role and never act in an organization; their secrets start with `dock_p_`. `list` also shows platform logins and legacy tokens that are both an organization's and the platform's; `split` keeps a legacy token the organization's and makes a platform token with the operator role that takes the platform over. Creating, revoking and splitting need `accounts.manage`, as a person |
| `user list\|create [--operator]\|disable\|enable\|delete\|reset-password\|platform-role EMAIL operator\|viewer\|none` | Users. `list` (admin) shows the organization's members with their role, status, two-factor sign-in (on or off), live sessions and usable tokens; the rest manage accounts, which are the platform's (operator), and `create` adds the new user to the organization in use. Users are named by email or id; `reset-password` sets `--password` or prints a generated one once, and ends the user's sessions and browser-login tokens (your own: `me password`). `disable` and `delete` end them too; tokens the user created by hand stay |
| `audit [-p -e -s] [--action prefix] [--platform]` | The organization's audit log; the filters are explicit flags (the link isn't used), `-s` needs `-p` and `-e`. `--platform` (operator) shows the platform's instead: servers, accounts, logins, backup settings, alert channels |
| `serve`, `admin bootstrap\|caddy-config\|registry-config --out DIR\|registry-storage NAME --endpoint URL --bucket B\|firewall apply\|remove\|backup [-o FILE\|-] [--include-registry] [--move]\|restore ARCHIVE [--old-server-becomes-node] [--print-config]\|update\|reset-2fa EMAIL\|password-sign-in on [--password-for EMAIL]` | Server side, run on the server with `--config`: the control plane daemon, the first operator token, Caddy's initial config (works before a node joins), the image registry's `config.yml` and `jwks.json` (deterministic for a given key and storage), a new registry's bucket (install.sh, keys on stdin; ADR 0034), the app firewall (root; installed as `dock-firewall.service`), and the control plane's state as one 0600 archive and back (ADR 0021; `--move` needs the daemon stopped and retires it, `restore` is run by `install.sh --restore`). `serve` exits 9 on a control plane that moved away. `admin update` is the updater, run as root by `dock-update.service` when the daemon asks for an update (ADR 0028). `admin reset-2fa` is the break-glass for someone who lost their second factor and recovery codes: it removes them and ends the user's sessions and browser-login tokens; they sign in with their password and set up a new factor (ADR 0057). `admin password-sign-in on` turns password sign-in back on when no identity provider works, and `--password-for` gives that account a new generated password, printed once (ADR 0065) |
| `node`, `node join TOKEN\|--token-file FILE`, `node set-control-plane HOST:PORT` | On a server that joins a control plane, with `--config`: the node daemon (`dock-node.service`: the link, the registry proxy on `127.0.0.1:5000`, Caddy's `ask`; no API, UI or database), and joining once with a token from `server add` (writes `node.key` and `node.toml` into the data dir, prints the assigned server name). `--token-file -` reads the token from stdin, keeping it out of the process list. `install.sh --node` runs both. `node set-control-plane HOST:PORT` points a joined node at a control plane that moved; the running daemon follows within 30 s |

Commands that start releases (`deploy`, `promote`, `rollback`, `restart`,
`service start`, `apply`, `--deploy`) follow them until they are live, failed or canceled; `--detach` returns
right after they are queued. Ctrl-C stops following, not the deploy.
`dock release wait N` follows a release again, or one started elsewhere (a
detached deploy, a git push).

With `--json`, a failed release printed by `release show`, `release wait`,
`deploy`, `rollback` or `restart` has a `logs` field with the end of what
explains it, the lines `dock logs --release N` and `dock logs --build
--release N` print:

```json
{"number": 3, "status": "failed", "error_code": "health_check_failed", "...": "...",
 "logs": {"container": ["2026-11-03T20:57:38Z [stderr] panic: DATABASE_URL is not set"],
          "build": ["Built dock/p1_e1_s1:3"]}}
```

`container` is the release's container output (time, `[stderr]` for
stderr, text); `build` the build or image pull output. Either is left out
when empty, and `logs` when both are.

Hints that name a command for a release or service (`dock release show 3
-p shop -e production -s web`) carry its `-p`, `-e` and `-s`, so they work
as printed outside the linked directory. A deploy refused because another
one is running (`deploy_in_progress`, exit 5) names `dock release wait` for
it.

### `dock run`

One-off commands ([ADR 0048](decisions/0048-one-off-commands.md)): a job
container from the service's active release, with its image, variables,
CPU and memory limits and its environment's private network, running
`/bin/sh -c COMMAND`. The running service is never touched. Needs deploy
scope; every run is audited (`service.run`) and listed by `dock runs`.

- The command is everything after `--`, which is required; only the
  service may come before it (`dock run web -- ls` or `dock run -s web --
  ls`). One argument is sent as it is (`dock run -- 'rake a && rake b'`);
  several are each quoted for the shell when needed (`dock run -- psql -c
  'select 1'` sends `psql -c 'select 1'`). Flags after `--`, `--json`
  included, are part of the command.
- `-i` sends stdin to the command, closing its input at stdin's end;
  without it stdin is never read. `-t` gives the command a terminal when
  stdin is one (otherwise a warning, and it runs without); with `-i` the
  local terminal is in raw mode until the run ends and follows resizes.
  `dock run -it web -- bin/rails console` is a console.
- `--timeout` (default 1h, at most 24h) stops the command after that long.
  `--volumes`: a stateful service's volumes are mounted read-only by
  default; `write` mounts them read-write next to the running service,
  after waiting for a snapshot of them (its ID is the run's `snapshot`; a
  failed snapshot refuses the run). `--no-snapshot` skips it. Databases'
  volumes are never mounted.
- `-d`/`--detach` starts the run in the background (no `-i` or `-t`),
  prints it and exits 0; `dock runs show ID` follows it. In a locked
  environment a run is refused (`environment_locked`, exit 5) and files
  an access request (`dock request wait ID`); approved, it runs in the
  background, without a terminal.
- Output: the command's stdout to stdout, its stderr to stderr. With
  `--json`, the command's stdout goes to stderr too, and stdout is only the
  run (`CommandRun`) as JSON once it ended (with `--detach`, as it
  started).
- Exit code: the command's own when it exited, with nothing printed. A run
  that ended without one (`timed_out`, `canceled`, `interrupted`, or
  `failed` because it couldn't start) is an error with the status as its
  `code`, exit 1; with `--json` the run is still printed on stdout. Ctrl-C
  without a terminal closes the connection, which stops the command, and
  exits 130. Errors before it starts (no release, a locked environment,
  permissions) are the usual exit codes.

### `dock job`

Scheduled jobs ([ADR 0054](decisions/0054-scheduled-jobs.md)): a cron
expression in a timezone and a command that runs like `dock run` in the
service's active image, its variables and private network, without
volumes. Reading jobs and their history needs read scope; setting,
removing, turning on or off and running now need deploy (audited as
`job.set`, `job.remove`, `job.run`) and are refused in a locked
environment (`environment_locked`, exit 5).

- `job set NAME --schedule CRON -- COMMAND...` creates the job or
  replaces every setting of the one with that name: a flag left out
  goes back to its default, and the job is on unless `--disabled`. The
  schedule is five fields (`"0 3 * * *"`) or `@hourly`, `@daily`,
  `@weekly`, `@monthly`, `@yearly`, read in `--timezone` (IANA, default
  UTC). The command after `--` is quoted as `dock run` quotes it.
  `--timeout` defaults to 1h, at most 24h; `--overlap skip` (the
  default) records a run that comes due while the last one still runs
  as `skipped`, `allow` runs both. It prints the job with its next time.
- Jobs from the service's dock.toml (`source` `repo`, shown as
  `dock.toml`) are changed in the file: `set` and `remove` are a
  conflict (exit 5). `enable`, `disable` and `run` work on them too.
  Removing a job while it runs is a conflict as well.
- `job run NAME` starts a run now and waits for it on the event stream,
  then prints its output's tail on stdout and how it ended on stderr.
  The exit code is the job's own, as with `dock run`; a run that ended
  without one (`timed_out`, `failed` because it couldn't start) is an
  error with the status as its `code`, exit 1. A job still running with
  overlap skip is a conflict (exit 5). `-d`/`--detach` prints the run as
  it started. Ctrl-C stops waiting, not the run. With `--json` stdout is
  the run (`CommandRun`), with its output, once it ended.
- `job history NAME` lists its newest 20 runs (scheduled, run now,
  skipped); `dock runs show ID` shows one with its output.

### `dock db`

The database panel ([guide](guide/data.md), [ADR 0016](decisions/0016-database-panel.md)).
Every subcommand but `policy` acts on the linked service (or `-p -e -s`).
Access is the data access policy's decision; a permission error (exit 3)
says which level was missing and the rule that decided it.

| Command | What it does | Needs |
|---|---|---|
| `db status` | Engine, version, setup state, who uses it, your access and why | level schema |
| `db setup [--yes]` | Create or rotate Dock's roles (`dock_reader`, `dock_writer`) | admin |
| `db settings [--database postgres\|mysql\|redis\|none\|detect] [--url-variable NAME]` | Mark a service as a database, or name its connection URL variable. Immediate, never a release | deploy |
| `db tables`, `db schema [TABLE]` (`describe`) | Tables; one table's (or every table's) columns, keys, indexes, constraints | level schema |
| `db rows TABLE [-w col=op:value ...] [--order col\|-col] [-n N] [--after CURSOR] [--count] [--reveal]` | One keyset page; prints the next cursor on stderr. Sensitive columns are masked; `--reveal` shows them if your access allows (audited). `--json` prints the page with `next` | level read |
| `db keys [PATTERN] [--type T] [--db N] [-n N] [--after CURSOR]` | Redis: one page of keys found with SCAN (glob, type), with type, length, sampled size and TTL; prints the next cursor on stderr | level read |
| `db key KEY [--db N] [-n N] [--after CURSOR] [--full] [--hex] [--reveal]` | Redis: a key's type, TTL and size, and one page of its value; a key matching a sensitive pattern needs `--reveal` (audited) | level read |
| `db query SQL\|-f FILE\|- [-n N] [--timeout D] [--csv]` | One read-only statement, streamed: a table, CSV, or NDJSON frames with `--json`. Default 1 000 rows, at most 100 000; timeout at most 60s | level read |
| `db history [--before ID] [-n N]` | Your past queries here, newest first | level read |
| `db saved list\|save NAME SQL\|run NAME\|delete NAME` | Your saved queries; `save` replaces one with the same name, `run` takes `query`'s flags | level read |
| `db changes list [--status S]\|show ID\|commit ID [--confirm ENV]\|reject ID [--reason]` | Changesets. Committing someone else's approves it | admin to approve |
| `db apply -f FILE [--dry-run\|--propose] [-m MSG] [--confirm ENV]` (`propose`) | Changes from JSON (rows, or Redis `key_changes`, [guide](guide/data.md#redis-keys)): preview the SQL or commands, or store and commit them in one transaction (level write), or leave them pending for an admin (level propose, or `--propose`) | level propose |
| `db policy show\|set [-p P [-e E]]` | The policy at the default, a project or an environment (explicit flags, not the link). `set deploy.production=read tokens.non-production=propose,visible ...` changes the named rules (`inherit` removes one); `--replace` drops the rest | admin to set |
| `db sensitive list\|set [schema.]table.column...\|--keys PATTERN...\|--clear` | Columns (on Redis, key patterns) masked for callers whose access says masked; `set` replaces the list, `list` shows it | admin to set |
| `db health` | Connections against the limit, cache hit ratio, size, and what needs a look (idle transactions, blocked sessions, duplicate, invalid and unused indexes, dead rows, full scans), with statements to copy | level schema |
| `db queries [--order total\|mean\|calls\|rows] [--limit N]` | The statements that cost the most, from `pg_stat_statements` (MySQL: statement digests; Redis: commands) | level read |
| `db sessions [--all]` | Sessions running, idle in a transaction or waiting on a lock, and the session that blocks them | level read |
| `db storage [--indexes\|--keys]` | Tables (or indexes) by size, with dead rows, scans and the last vacuum; `--keys`: Redis key prefixes by size, estimated from a sample (level read) | level schema |
| `db queries enable` | Turn on query stats: for a Postgres template database, stage adding `pg_stat_statements` to its start command (it applies with the next deploy, which restarts the database, and Dock creates the extension then); otherwise create the extension as the owner | deploy (template), else admin |
| `db queries reset [--yes]` | Set query stats back to zero | admin |
| `db cancel SESSION [--terminate] [--confirm ENV]` | Cancel a session's running query, or end the session (its connection and open transaction). Ending one on production needs `--confirm`. Redis can only end a client (`--terminate`) | level write |

`health`, `queries`, `sessions` and `storage` read the database's own
statistics live, as Dock's `dock_monitor` role; nothing is stored, and
counters count since their last reset or the server's start. Statement
values are redacted when your access masks sensitive columns.

Filters (`-w`, `--filter`): `col=value` (equals), `col=op:value` with `eq`,
`ne`, `lt`, `lte`, `gt`, `gte`, `like`, `ilike`, `in` (comma-separated), or
`col=is_null` / `col=not_null`. Values are the database's text input (binary as `\x` hex).

## Shell completion

`dock completion bash|zsh|fish|powershell` prints a completion script (see
its `--help` for where to put it). Besides commands and flags, it completes
names from the server: projects (`-p`, `project archive`), environments
(`-e`, `promote`, `environment clone|lock|unlock`), services (`-s`,
`service show|stop|start`, `connect`, `runs`, `backup list`), release
numbers (`release show|wait`, `rollback`, `logs --release`), access
requests (`request show|wait`), runs (`runs show`), snapshots (`volume
snapshot restore|delete`) and log drains (`logs drain remove|test`); numbers
newest first with their status. The `-p` and `-e` already typed, then the
directory's link, narrow the list. Completion uses the same server, token
and organization as the command would, and completes nothing (silently)
when there is no server, no token or no answer within 3 seconds. It costs
nothing at start-up: names are only fetched under the completion script's
hidden `__complete` command.

## CLI-only build

The release binary `dock` is the CLI, the daemon and the node runtime. A
laptop or CI runner only needs the client: `make build-cli` builds it with
the `dockcli` build tag, which leaves out `serve`, `node`, `admin` and the
hidden `openapi` command and with them Docker, BuildKit, Caddy, SQLite and
the API server.

```sh
make build-cli   # bin/dock-cli for this machine, plus bin/dock-cli-<os>-<arch>
                 # for darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64
```

| | `dock` | `dock-cli` |
|---|---|---|
| Size (linux/amd64) | 66 MB with the UI | 10 MB |
| `dock context list` | 9 ms, 39 MB RSS | 3 ms, 13 MB RSS |

Measured on the development VM, 2026-11-10 (median of 30 runs).

Every other command behaves the same in both, except `dock self-update`,
which only replaces a `dock-cli` (a server updates with `dock update apply`). Releases publish `dock-cli`
for these four platforms; see [install the CLI](guide/install.md#the-cli).
