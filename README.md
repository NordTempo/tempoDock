# Tempo Dock

A self-hosted, Railway-style deploy platform. The CLI and binary are `dock`.

Deploy Docker apps to your own servers from a CLI or a web UI, with projects,
environments, services and variables. Built so AI agents can deploy safely
without being able to break or destroy things. Lightweight enough that the
platform never competes with the apps it hosts.

Built by NordTempo, for NordTempo first. Open source and paid hosting are
deliberately kept possible (see [vision](docs/vision.md)).

## Status

M0 to M2 are done and running on a homelab test server; M3 (multiple
servers) and the database panel (M5) are in progress. See the
[roadmap](docs/roadmap.md#status-2026-10-22).

The database panel also ships on its own as **tempoBase** (`tempobase`), a
small self-hosted server for browsing, querying and safely changing
Postgres data, built from this repository and released with Dock
([guide](docs/tempobase/README.md), [ADR 0024](docs/decisions/0024-tempobase-standalone.md)).

## Local development

Needs Go (see `go.mod`), Node 24 and pnpm. Docker, Caddy and BuildKit are only
needed for integration tests and real deploys.

```sh
make dev               # daemon on an in-memory fake runtime, API on 127.0.0.1:7070
make web && make build # build both UIs, then bin/dock with its UI embedded
make build-cli         # bin/dock-cli: the client alone (no daemon), 8.5 MB
make build-tempobase   # bin/tempobase with its UI embedded
make tempobase-deps    # tempobase links none of Dock's daemon packages
make test              # unit tests
make test-integration  # tests against the local Docker Engine and Caddy
make lint
make generate          # sqlc queries, OpenAPI spec and the UI's API client
```

`cd web && pnpm dev` serves the UI with hot reload, proxying `/api` to the
daemon. Install on a server from a release with
[`deploy/install.sh`](deploy/install.sh), and the CLI on laptops and CI
with `install-cli.sh` ([install guide](docs/guide/install.md#install)).
`make release-local VERSION=v0.0.0-dev` builds a release's artifacts into
`dist/`; pushing a `v*` tag publishes one.

## Docs

| Doc | What it covers |
|---|---|
| [Guide](docs/guide/README.md) | Install, deploy, variables, domains, agents, API: how to use it |
| [tempoBase guide](docs/tempobase/README.md) | Install and run the standalone Data panel: connections, people, agents, backups |
| [Vision](docs/vision.md) | Who it's for, goals, non-goals, principles |
| [Experience](docs/experience.md) | How the CLI and UI should feel, performance targets |
| [CLI](docs/cli.md) | CLI conventions, `--json`, exit codes |
| [dock.toml](docs/dock-toml.md) | The optional repo config file |
| [Performance](docs/performance.md) | Budgets, rules and `make perf` |
| [Tech stack](docs/tech-stack.md) | The chosen technologies at a glance |
| [Architecture](docs/architecture.md) | Components, data model, deploy flow, code layout |
| [Safety model](docs/safety-model.md) | Permissions, non-destructive defaults, keeping the box alive |
| [Roadmap](docs/roadmap.md) | Milestones, where we start, what "done" means |
| [Decisions](docs/decisions/README.md) | Architecture decision records (the "why") |
| [Open questions](docs/open-questions.md) | What we still need to decide |
