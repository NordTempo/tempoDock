# internal/cli

Every `dock` command (Cobra). Client commands talk to a daemon over HTTP
with `apitypes` values (`get[T]`, `send[T]`, `del` on the `session` from
`g.connect()`); server commands (`serve`, `node`, `admin`, `openapi`)
run the daemon in-process. The contract scripts and agents rely on is
[docs/cli.md](../../docs/cli.md): read it before adding a command or flag.

## Two builds

`make build-cli` builds with `-tags dockcli`: the client alone, without the
daemon's dependencies (Docker, BuildKit, Caddy, SQLite, Huma).

- A file that imports `core`, `api`, `store`, `node` or another daemon
  package starts with `//go:build !dockcli` and is reached only through
  `serverCommands()` (`server_cmds.go`; `cli_only.go` is the client's
  empty version).
- Client commands import `apitypes`, `clientconfig`, `cli/apiclient`,
  `cli/output` and small leaf packages (`pack`, `repoconfig`,
  `projectfile`, `cron`, `runframe`, `version`), nothing heavier. Check
  with `go list -tags dockcli -deps ./cmd/dock`.
- Nothing runs at start-up that `--help` doesn't need: no network, no
  config reads in `init`.

## Output

- Print results with `g.io.Print(v, human)`: `v` is the `--json` value,
  `human` the table or text. Progress goes to stderr and is silent with
  `--json`.
- Errors are `*output.Error` with an exit code from `output/errors.go`,
  a message and a `Hint` naming the command that fixes it. A bad flag
  value found in `RunE` is `output.ExitUsage` too: Cobra's own flag
  errors are mapped to it, but by `RunE` the root has started and a
  plain `fmt.Errorf` exits 1 without a hint.
- `--json` shapes only ever gain fields. Same words as the API and UI.
- A new server action ships with its UI (ADR 0026), or the commit says
  which follow-up adds it.

Tests: `commands_test.go` runs commands against the real API and core on
the fake runtime (`testServer`).
