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
  confirmation (`discard`, `domain remove`, `service archive`) need `--yes`.

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
| 7 | `unreachable` | Could not reach the server (or its proxy answered 502/503/504) |
| 8 | `incompatible` | CLI and server versions don't work together |

API errors are RFC 7807 `application/problem+json` bodies. The exit code
follows the HTTP status (400/422 → 2, 401/403 → 3, 404/410 → 4, 409/412/423 →
5, 426 → 8, 502/503/504 → 7, else 1), except that these problem `code`
values take precedence: `build_failed`, `deploy_failed`,
`health_check_failed` → 6, `version_incompatible` → 8.

## Contexts and environment

A context is a named server URL plus API token, like a kubectl context:
`dock context add|list|use|current|remove`, and `dock logout` removes
the token of the context in use.

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

Log in with `dock login <server-url>`: on a terminal it prompts for the
token without echo. Scripts pipe it in, so it stays out of shell history and
the process list:

```sh
dock login https://dock.example.com --token-stdin < token-file
```

`--token <token>` still works but is not recommended.

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
| `login <url> [--token-stdin]` | Verify a token against `/version` and `/me` (prompted for on a terminal, or read from stdin), store it as a context and use it. Warns when the server runs another version |
| `whoami` | Who the token belongs to and its scope |
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
| `project list\|create`, `environment list\|create` | Projects and environments (`env` is variables) |
| `token list\|create NAME --scope\|revoke ID`, `user list\|create`, `server list` | Administration |
| `audit [-p -e -s] [--action prefix]` | Audit log; the filters are explicit flags (the link isn't used), `-s` needs `-p` and `-e` |

Commands that start releases (`deploy`, `rollback`, `restart`, `apply`,
`--deploy`) follow them until they are live or failed; `--detach` returns
right after they are queued. Ctrl-C stops following, not the deploy.
