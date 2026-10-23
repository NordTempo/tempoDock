# internal/tempobase

tempoBase, the Data panel as its own binary (`cmd/tempobase`, ADR 0024).
The panel's rules live once in `internal/datapanel`; Dock's core and this
package are its two hosts.

```
internal/datapanel/           policy, setup, changesets, cursors, data audit,
                              behind the Host interface; no host imports
internal/datapanel/datahttp/  what both hosts' data endpoints share
internal/data/pg/             the Postgres engine both use
internal/tempobase/           the App: people, sign-in, tokens, connections,
                              policy, audit, the datapanel.Host
  tbhttp/                     Huma API and embedded UI
  tbstore/                    its own SQLite (same rules as internal/store)
  tbmcp/                      MCP endpoint over the App
  tbtypes/                    its JSON contract; panel types are apitypes'
```

## Boundaries

- tempoBase never links Dock's daemon: no `core`, `api`, `store`, `node`,
  `mcp`, `cli`, Docker, BuildKit or gRPC. `make tempobase-deps` fails on
  it in CI. Code both need goes in `datapanel`, `datahttp`, `data`,
  `httpkit`, `mcpkit` or `secrets`, never in a host.
- `datapanel` must not import either host.
- A fix to the panel's behaviour goes in `datapanel` so both hosts get it.
  Host-only differences (who a caller is, where state is stored, words)
  stay in the host.

## The data API is one contract

The web panel (`web/src/features/data`) is typed from Dock's spec, so
tempoBase's data endpoints take and return exactly Dock's shapes; only the
path differs (`/connections/{c}/data` vs `/services/{s}/data`).
`tbhttp/contract_test.go` compares the two specs. Change a data endpoint
in both hosts together.

## Generating

- `go generate ./internal/tempobase/tbstore` after a query or migration.
- `go generate ./internal/tempobase/tbhttp` writes
  `web/tempobase/openapi.json`; then `pnpm gen:api` in `web/` (or
  `make generate` for everything).
- `make build-tempobase`, and `make perf` checks its size and benchmarks.
