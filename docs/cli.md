# CLI conventions

What scripts, CI and agents can rely on. The goals behind these are in
[experience](experience.md).

## Output

- Results go to stdout. Errors, warnings and progress go to stderr.
- `--json` works on every command. stdout is then exactly one JSON value
  whose shape is part of the command's contract: fields are only ever added,
  never renamed or removed. Tokens are never printed.
- With `--json`, an error is one JSON object on stderr:

  ```json
  {"error": {"code": "not_found", "exitCode": 4, "message": "context \"x\" not found", "hint": "Run `platform context list` to see contexts."}}
  ```

  `code` is the server's machine-readable reason when it gave one (e.g.
  `health_check_failed`), otherwise the exit code's name from the table below.
- Human output uses aligned columns and plain status glyphs (`✓ ✗ ! ·`).
  Color only when the stream is a terminal and `NO_COLOR` is unset.
- The CLI only prompts when stdin is a terminal. Otherwise missing input is a
  usage error naming the flag to pass.

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
`platform context add|list|use|current|remove`, and `platform logout` removes
the token of the context in use.

The server and token come from, in order:

1. `PLATFORM_URL` and `PLATFORM_TOKEN`. Contexts are ignored entirely, so no
   config file is needed (CI, agent sandboxes).
2. The context named by `PLATFORM_CONTEXT`.
3. The context of the linked directory (see below).
4. The current context.

`PLATFORM_TOKEN` also overrides the token of a context picked by 2-4.
`platform context current` shows what the current directory resolves to and
why.

## Config file and links

`$XDG_CONFIG_HOME/platform/config.json` (default
`~/.config/platform/config.json`), mode 0600, replaced atomically on write.

`platform link` records which project, environment and service a directory
deploys to. Links are stored in the config file by absolute path, not in the
repo. Commands use the link of the current directory or its nearest linked
parent, like git finds its repository. `platform unlink` removes that link.
