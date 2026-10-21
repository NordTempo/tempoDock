# Tech stack

The chosen technologies at a glance. The reasoning lives in the linked
decision records.

| Area | Choice | Decision |
|---|---|---|
| Daemon + CLI language | **Go**, one static binary for both | [0001](decisions/0001-go-for-daemon-and-cli.md) |
| Packaging | **Single binary** with the web UI embedded (`go:embed`) | [0002](decisions/0002-single-binary-embedded-ui.md) |
| Platform state | **SQLite** (WAL), `modernc.org/sqlite`, **sqlc**, embedded SQL migrations | [0003](decisions/0003-sqlite-for-platform-state.md) |
| Reverse proxy | **Caddy** as a separate process, configured only by the daemon | [0004](decisions/0004-caddy-as-separate-proxy.md) |
| Domains + TLS | Rendered from the DB, on-demand TLS gated by the daemon | [0009](decisions/0009-dynamic-domains-and-tls.md) |
| API | **Huma** (OpenAPI 3.1 from Go types), SSE for live data | [0005](decisions/0005-openapi-api-with-generated-clients.md) |
| Builds | Source, git push, CI image or promotion; capped **BuildKit** on builder servers | [0006](decisions/0006-every-way-to-build.md) |
| Servers | One control plane, nodes dial out; each environment lives on one server | [0010](decisions/0010-environments-on-servers.md) |
| Databases | Services from templates; stateful services recreate, one replica, tool-native backups | [0011](decisions/0011-stateful-services-and-databases.md) |
| Service config | Platform DB, plus optional `dock.toml` in the repo | [0007](decisions/0007-config-db-plus-repo-file.md) |
| Future options | Single-tenant core with users and roles, internal node boundary | [0008](decisions/0008-keep-doors-open.md) |

## Supporting choices

These follow from the above and don't need their own records.

| Area | Choice |
|---|---|
| CLI framework | Cobra |
| Docker | Official Go SDK (`github.com/docker/docker/client`) |
| Zero-config builds | Railpack (Railway's open-source builder) when there is no Dockerfile |
| UI | React, Vite, Tailwind, TanStack Router, TanStack Query |
| UI API client | `openapi-typescript` + `openapi-fetch`, generated from the spec |
| Secrets at rest | AES-GCM, key file on disk (`0600`), outside the database |
| Host networking for the API | Tailscale (recommended), or public with login |
| Image registry | CNCF Distribution (`registry`) next to the control plane, or any external registry |
| Backups (M5) | restic to S3-compatible storage |
| Go tooling | `gofmt`, `go vet`, `golangci-lint` |
| UI tooling | pnpm, TypeScript strict, Biome |
| CI | GitHub Actions (unit tests, Docker integration tests, build) |

## Rejected

- **TypeScript / Bun on the server:** shares types with the UI natively, but
  costs RAM and needs a runtime or a large compiled binary. See 0001.
- **Rust:** smallest footprint, but much slower iteration. See 0001.
- **Kubernetes / k3s, Swarm, Nomad:** an orchestration layer we don't need on
  one server, with real overhead.
- **Postgres for platform state:** another service to run and back up, for data
  that fits easily in SQLite.
- **Convex or any hosted backend:** conflicts with owning our data and running
  fully self-hosted.
