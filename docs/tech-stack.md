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
| Service config | Platform DB, plus optional `dock.toml` in the repo; whole projects as TOML project files | [0007](decisions/0007-config-db-plus-repo-file.md), [0049](decisions/0049-apply-and-compose-import.md) |
| Tenancy | Organizations with members and roles, platform operators, internal node boundary | [0008](decisions/0008-keep-doors-open.md), [0027](decisions/0027-organizations.md) |
| Agents | MCP endpoint over core (official Go SDK) | [0017](decisions/0017-mcp-endpoint.md) |
| Data panel | Postgres through pgx; also the standalone tempoBase binary | [0016](decisions/0016-database-panel.md), [0024](decisions/0024-tempobase-standalone.md) |
| Web analytics | DuckDB in its own binary and container (`dock-analytics`, CGO), never linked into `dock` | [0037](decisions/0037-analytics.md) |
| Image registry | CNCF Distribution (`registry:3`) next to the control plane, on disk or S3-compatible storage | [0014](decisions/0014-node-protocol.md), [0034](decisions/0034-registry-object-storage.md) |
| Backups | restic in job containers, to the server or a storage connection | [0019](decisions/0019-templates-and-backups.md), [0039](decisions/0039-backups-on-storage-connections.md) |
| Sign-in | Passwords, passkeys and TOTP (go-webauthn), OIDC providers (go-oidc) | [0057](decisions/0057-two-factor-sign-in.md), [0065](decisions/0065-sign-in-is-the-platforms.md) |

## Supporting choices

These follow from the above and don't need their own records.

| Area | Choice |
|---|---|
| CLI framework | Cobra |
| Docker | Official Go SDK (`github.com/moby/moby/client`) |
| Zero-config builds | Railpack (Railway's open-source builder) when there is no Dockerfile |
| Node link | TLS 1.3 with pinned keys and reverse HTTP/2, standard library only |
| UI | React, Vite, Tailwind, TanStack Router, TanStack Query; CodeMirror and Glide Data Grid in the Data panel, rrweb for session replays (route-split) |
| UI API client | `openapi-typescript` + `openapi-fetch`, generated from the spec |
| Secrets at rest | AES-GCM, key file on disk (`0600`), outside the database |
| Host networking for the API | Tailscale (recommended), or a public address through Caddy |
| Tunnels | cloudflared, run by Dock or by systemd |
| Landing page | Astro, static, deployed on Dock (`site/`) |
| Go tooling | `gofmt`, `goimports`, `go vet`, `golangci-lint` |
| UI tooling | pnpm, TypeScript strict, Biome |
| CI | GitHub Actions on self-hosted runners: lint, unit, race and Docker integration tests, the analytics module, `make perf`, the UI and generated files |

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
