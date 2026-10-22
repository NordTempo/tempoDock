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
  `user delete`, `server revoke`, `server remove`, `alerts channel remove`, `db setup`) need `--yes`, and commits to production data
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
| 5 | `conflict` | Already exists, or another operation (e.g. a deploy) holds the lock |
| 6 | `deploy_failed` | Build or health check failed. Traffic was not switched |
| 7 | `unreachable` | Could not reach the server (or its proxy answered 502/503/504), or the server an environment is placed on is offline (`server_offline`) |
| 8 | `incompatible` | CLI and server versions don't work together |

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
3. The CLI gets its own token: named `cli: <hostname>`, your role as
   scope, valid for 90 days, listed on the Tokens page as from `dock login`.
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

## Commands

`dock <command> --help` has the details.

| Command | What it does |
|---|---|
| `login [url] [--no-browser \| --token-stdin]` | Log in through the browser (on a terminal, or with `--no-browser`), or verify a token from stdin; store it as a context and use it. Warns when the server runs another version |
| `version` | The CLI's version and, when a server is configured, the server's (one unauthenticated request; nothing without a server), with the command that installs the matching CLI when they differ |
| `logout` | Remove the context's token; revoke it on the server if it came from a browser login |
| `whoami` | Who the token belongs to and its scope |
| `mcp [--show-token] [--name N]` | Print the MCP client config for the context's server ([agents](guide/agents.md#mcp)): the endpoint URL, a `claude mcp add` command and an `mcp.json` entry. The token is `${DOCK_TOKEN}` unless `--show-token` |
| `me password [--password-stdin]` | Change your own password: prompts for the current one and the new one twice, or reads both from stdin (one per line). Needs a browser-login token, which acts for you; API tokens have no password. Your UI sessions and other browser-login tokens end; the CLI's own token stays valid |
| `link [-p -e -s]`, `unlink` | Link the directory. Pickers on a terminal; otherwise `--project` (and `--environment` when there are several) |
| `deploy [dir] [--image ref] [-d]` | Pack and stream the directory (or deploy an image) and follow build, start, health check and cutover. A `--service` that doesn't exist yet is created. An image deploy sets the service's `image` setting; a source deploy clears it. Exit 6 when the release fails. When the environment's server is offline or revoked, a source deploy stops before packing anything (`server_offline`, exit 7) |
| `promote FROM TO [-s service...] [-d]` | Deploy the images serving in environment FROM to TO (same project), by digest, with TO's own variables and settings; nothing is rebuilt. One service at a time, stopping at the first failure. Services missing in TO (or with nothing running in FROM) are skipped and listed. Needs deploy on TO. Exit 6 when a release fails, exit 4 when nothing was promoted (every service skipped; the skipped list is still printed, and with `--json` the result still goes to stdout) |
| `status [-a]` | The service's state, releases, domains and staged changes; `-a` (or no service) for the whole environment. On a server that is offline or revoked the state is the last known one and says so ("server t3node is offline: last known running"; `server_offline`, `server` and `server_status` in the JSON status) |
| `logs [-f] [-n N] [--since] [-r N] [--build]` | Service output, or a build log |
| `releases`, `release list\|show N` | Release history and one release with its events |
| `rollback [N]`, `release rollback [N]` | New release from an earlier one; default the one that served before the active one. Says which release it copies and that release's image and config |
| `restart` | New release identical to the active one |
| `git connect OWNER/REPO [--branch B] [--dir D] [--token-stdin\|--remove-token] [--status] [--rotate-secret]` | Deploy on every push to a GitHub branch ([guide](guide/deploying.md#deploy-on-git-push)). Prints the webhook settings for GitHub and its secret, once (again only with `--rotate-secret`). Running it again changes the branch, directory, token or statuses. The token is read from stdin only and never printed |
| `git status`, `git deploy [--ref R]`, `git disconnect [-y]` | The GitHub source: webhook URL, token set or not, queued push, latest delivery; queue the branch's head (or a SHA, branch or tag) now, even if live; remove the connection and its token |
| `env list [--reveal]` | Shared and service variables |
| `env set K=V... \| unset K... \| import FILE [--shared] [--deploy]` | Stage variable changes; `--shared` for environment-level, `--deploy` to apply right away |
| `changes [--reveal]`, `apply` | Show or apply (and follow the resulting releases) the environment's staged changes |
| `discard [KEY...] [-s service] [--shared]` | Drop staged changes: all in the environment; with `-s`, that service's own; with KEYs, those variables' (the service's, or shared with `--shared`) |
| `domain list [-w]\|add HOST [--redirect-to]\|generate\|remove HOST` | Domains; `list -w` adds the URL, last DNS check and what the hostname resolved to; `add` prints the DNS record and waits (`--timeout`, default 5m) for DNS and the certificate, then prints the URL |
| `volume list\|add NAME PATH` | Volumes (staged) |
| `service list\|create\|show\|update\|archive` | Services; `update` stages only the settings passed. `create NAME --template postgres\|redis` creates a database from a template and deploys it ([guide](guide/databases.md)) |
| `backup list\|run [-d]\|restore --snapshot ID [--as NAME] [-d]\|schedule [--hour H\|--default-hour] [--on\|--off]` | A stateful service's backups: list runs and snapshots, back up now (waits; `--detach` doesn't), restore into a new service (template databases), change the daily schedule. Exit 1 when the awaited run fails |
| `backup status [--reveal]`, `backup configure [--repository URL] [--access-key-id ID] [--secret-access-key-stdin] [--region R] [--keep-daily N] [--keep-weekly N] [--keep-monthly N] [--hour H]` | Where backups go, retention and the default hour; `configure` (admin) sets them and prints the repository password the first time; `--reveal` (admin) shows it again |
| `project list [--all]\|create\|archive <name>`, `environment list\|create [--kind]` | Projects and environments (`env` is variables). `project archive` (admin) archives every service in the project and hides it from `project list` (`--all` shows it). An environment's kind (production or non-production) defaults from its name and never changes |
| `project pin <project> --kind production\|non-production --server NAME` | Admin: choose the server a project's environments of a kind go to, before any exist |
| `server list`, `server show NAME` | Servers: link status (`online`, `offline`, `incompatible`, `joining`, `revoked`), version, ingress, accepted kinds, memory reserved; `show` (admin) adds base domain, DNS target, what's placed and current usage |
| `server add NAME [--production] [--non-production] [--builder]` | Admin: create a one-time join token (valid for an hour) and print the exact `install.sh --node` command to run on the new server. The token is shown once |
| `server update NAME [--production\|--no-production] [--non-production\|--no-non-production] [--disable-placement\|--enable-placement] [--builder\|--no-builder] [--base-domain D]` | Admin: what the server accepts, whether it builds, and the base domain of generated hostnames (`""` removes it) |
| `server revoke NAME`, `server rotate-key NAME`, `server remove NAME` | Admin: forget a server's key and drop its link (it must join again; its apps keep running); have an online server switch to a new key; remove a server no live project uses (archived projects' environments move to the control plane's server) |
| `token list\|create NAME --scope\|update ID\|revoke ID` | Tokens (admin). `create` and `update` take `--data-production`, `--data-non-production` (`none`, `schema`, `read`, `propose`, `write`) and `--data-sensitive` (`masked`, `visible`): the token's own database access instead of the data policy's tokens rule; `update --data-clear` removes it |
| `user list\|create\|disable\|enable\|delete\|reset-password` | Users (admin). Users are named by email or id. `list` shows status, live sessions and usable tokens; `reset-password` sets `--password` or prints a generated one once, and ends the user's sessions and browser-login tokens (your own: `me password`). `disable` and `delete` end them too; tokens the user created by hand stay |
| `audit [-p -e -s] [--action prefix]` | Audit log; the filters are explicit flags (the link isn't used), `-s` needs `-p` and `-e` |
| `serve`, `admin bootstrap\|caddy-config\|registry-config --out DIR\|firewall apply\|remove` | Server side, run on the server with `--config`: the control plane daemon, the first admin token, Caddy's initial config (works before a node joins), the image registry's `config.yml` and `jwks.json` (deterministic for a given key), and the app firewall (root; installed as `dock-firewall.service`) |
| `node`, `node join TOKEN\|--token-file FILE` | On a server that joins a control plane, with `--config`: the node daemon (`dock-node.service`: the link, the registry proxy on `127.0.0.1:5000`, Caddy's `ask`; no API, UI or database), and joining once with a token from `server add` (writes `node.key` and `node.toml` into the data dir, prints the assigned server name). `--token-file -` reads the token from stdin, keeping it out of the process list. `install.sh --node` runs both |

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
