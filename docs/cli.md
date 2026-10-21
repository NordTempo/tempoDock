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
- Streaming commands (`dock logs`) print one JSON object per line instead.
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
  `user delete`) need `--yes`.

## Exit codes

Stable; never renumbered.

| Code | Name | Meaning |
|---|---|---|
| 0 | `ok` | Success |
| 1 | `error` | Anything not covered below |
| 2 | `usage` | Bad flags, arguments or input. Nothing was done |
| 3 | `auth` | No server configured, no token, or the token lacks permission |
| 4 | `not_found` | The project, service, context, link, ... doesn't exist |
| 5 | `conflict` | Already exists, or another operation (e.g. a deploy) holds the lock |
| 6 | `deploy_failed` | Build or health check failed. Traffic was not switched |
| 7 | `unreachable` | Could not reach the server (or its proxy answered 502/503/504), or the server an environment is placed on is offline (`server_offline`) |
| 8 | `incompatible` | CLI and server versions don't work together |

API errors are RFC 7807 `application/problem+json` bodies. The exit code
follows the HTTP status (400/422 → 2, 401/403 → 3, 404/410 → 4, 409/412/423 →
5, 426 → 8, 502/503/504 → 7, else 1), except that these problem `code`
values take precedence: `build_failed`, `deploy_failed`,
`health_check_failed` → 6, `version_incompatible` → 8.

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
2. In the browser, sign in if needed, check the code matches the terminal
   and approve. The page shows the CLI's host, OS and IP: deny a login you
   didn't start.
3. The CLI gets its own token: named `cli: <hostname>`, your role as
   scope, valid for 90 days, listed on the Tokens page as from `dock login`.
   Links expire after 10 minutes; Ctrl-C cancels.

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
| `logout` | Remove the context's token; revoke it on the server if it came from a browser login |
| `whoami` | Who the token belongs to and its scope |
| `me password [--password-stdin]` | Change your own password: prompts for the current one and the new one twice, or reads both from stdin (one per line). Needs a browser-login token, which acts for you; API tokens have no password. Your UI sessions end; the CLI's token stays valid |
| `link [-p -e -s]`, `unlink` | Link the directory. Pickers on a terminal; otherwise `--project` (and `--environment` when there are several) |
| `deploy [dir] [--image ref] [-d]` | Pack and stream the directory (or deploy an image) and follow build, start, health check and cutover. A `--service` that doesn't exist yet is created. An image deploy sets the service's `image` setting; a source deploy clears it. Exit 6 when the release fails |
| `status [-a]` | The service's state, releases, domains and staged changes; `-a` (or no service) for the whole environment |
| `logs [-f] [-n N] [--since] [-r N] [--build]` | Service output, or a build log |
| `releases`, `release list\|show N` | Release history and one release with its events |
| `rollback [N]`, `release rollback [N]` | New release from an earlier one; default the one that served before the active one. Says which release it copies and that release's image and config |
| `restart` | New release identical to the active one |
| `env list [--reveal]` | Shared and service variables |
| `env set K=V... \| unset K... \| import FILE [--shared] [--deploy]` | Stage variable changes; `--shared` for environment-level, `--deploy` to apply right away |
| `changes [--reveal]`, `apply` | Show or apply (and follow the resulting releases) the environment's staged changes |
| `discard [KEY...] [-s service] [--shared]` | Drop staged changes: all in the environment; with `-s`, that service's own; with KEYs, those variables' (the service's, or shared with `--shared`) |
| `domain list [-w]\|add HOST [--redirect-to]\|generate\|remove HOST` | Domains; `list -w` adds the URL, last DNS check and what the hostname resolved to; `add` prints the DNS record and waits (`--timeout`, default 5m) for DNS and the certificate, then prints the URL |
| `volume list\|add NAME PATH` | Volumes (staged) |
| `service list\|create\|show\|update\|archive` | Services; `update` stages only the settings passed |
| `project list [--all]\|create\|archive <name>`, `environment list\|create [--kind]` | Projects and environments (`env` is variables). `project archive` (admin) archives every service in the project and hides it from `project list` (`--all` shows it). An environment's kind (production or non-production) defaults from its name and never changes |
| `project pin <project> --kind production\|non-production --server NAME` | Admin: choose the server a project's environments of a kind go to, before any exist |
| `server list`, `server show NAME`, `server update NAME [--production\|--no-production] [--non-production\|--no-non-production] [--disable-placement\|--enable-placement]` | Servers: status, version, accepted kinds, memory reserved, what's placed, current usage. `show` and `update` are admin |
| `token list\|create NAME --scope\|revoke ID` | Tokens (admin) |
| `user list\|create\|disable\|enable\|delete\|reset-password` | Users (admin). Users are named by email or id. `list` shows status, live sessions and usable tokens; `reset-password` sets `--password` or prints a generated one once, and ends the user's sessions (your own: `me password`) |
| `audit [-p -e -s] [--action prefix]` | Audit log; the filters are explicit flags (the link isn't used), `-s` needs `-p` and `-e` |
| `serve`, `admin bootstrap\|caddy-config\|firewall apply\|remove` | Server side, run on the server with `--config`: the daemon, the first admin token, Caddy's initial config, and the app firewall (root; installed as `dock-firewall.service`) |

Commands that start releases (`deploy`, `rollback`, `restart`, `apply`,
`--deploy`) follow them until they are live or failed; `--detach` returns
right after they are queued. Ctrl-C stops following, not the deploy.

## CLI-only build

The release binary `dock` is the CLI, the daemon and the node runtime. A
laptop or CI runner only needs the client: `make build-cli` builds it with
the `dockcli` build tag, which leaves out `serve`, `admin` and `openapi` and
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

Every other command behaves the same in both.
