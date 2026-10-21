# Tempo Dock

A self-hosted, Railway-style deploy platform. The CLI and binary are `dock`.

Deploy Docker apps to your own servers from a CLI or a web UI, with projects,
environments, services and variables. Built so AI agents can deploy safely
without being able to break or destroy things. Lightweight enough that the
platform never competes with the apps it hosts.

Built by NordTempo, for NordTempo first. Open source and paid hosting are
deliberately kept possible (see [vision](docs/vision.md)).

## Status

M0 and M1 in progress (see [roadmap](docs/roadmap.md)). Deploys from source or
image, health-gated cutover, rollback, staged variables and domains work end
to end against real Docker, BuildKit and Caddy.

## Local development

Needs Go (see `go.mod`), Node 24 and pnpm. Docker, Caddy and BuildKit are only
needed for integration tests and real deploys.

```sh
make dev               # daemon on an in-memory fake runtime, API on 127.0.0.1:7070
make web && make build # build the UI, then bin/dock with the UI embedded
make test              # unit tests
make test-integration  # tests against the local Docker Engine and Caddy
make lint
make generate          # sqlc queries, OpenAPI spec and the UI's API client
```

`cd web && pnpm dev` serves the UI with hot reload, proxying `/api` to the
daemon. Install on a server with [`deploy/install.sh`](deploy/install.sh).

## Docs

| Doc | What it covers |
|---|---|
| [Vision](docs/vision.md) | Who it's for, goals, non-goals, principles |
| [Experience](docs/experience.md) | How the CLI and UI should feel, performance targets |
| [CLI](docs/cli.md) | CLI conventions, `--json`, exit codes |
| [dock.toml](docs/dock-toml.md) | The optional repo config file |
| [Tech stack](docs/tech-stack.md) | The chosen technologies at a glance |
| [Architecture](docs/architecture.md) | Components, data model, deploy flow, code layout |
| [Safety model](docs/safety-model.md) | Permissions, non-destructive defaults, keeping the box alive |
| [Roadmap](docs/roadmap.md) | Milestones, where we start, what "done" means |
| [Decisions](docs/decisions/README.md) | Architecture decision records (the "why") |
| [Open questions](docs/open-questions.md) | What we still need to decide |
