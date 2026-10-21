# Tech stack

Status: **proposed**, to be confirmed by the M0 spike (see [roadmap](roadmap.md)).

## Constraints

1. **Low idle footprint.** RAM and CPU on the VPS belong to the apps.
2. **Fast and simple install.** Ideally one binary plus a systemd unit.
3. **First-class Docker integration.**
4. **Type safety end to end**, including the API boundary to the UI.
5. **Development speed**, mostly with agents writing code.

## Language for the daemon and CLI

Memory numbers are rough estimates for a small idle HTTP service and must be
verified in the M0 spike.

| | Go | Rust | TypeScript (Bun) | TypeScript (Node) |
|---|---|---|---|---|
| Idle RAM (est.) | ~10-25 MB | ~5-15 MB | ~30-60 MB | ~40-80 MB |
| Distribution | Static binary, trivial cross-compile | Static binary, cross-compile is more work | `bun build --compile`, large binary | Needs a runtime |
| Docker client | Official SDK (Docker is written in Go) | `bollard`, community | `dockerode`, community, loosely typed | Same |
| Compile speed | Fast | Slow | n/a | n/a |
| Agent productivity | High | Medium | High | High |
| Ecosystem fit | Docker, Caddy, containerd, BuildKit are all Go | Good | Good | Good |

### Recommendation: Go

- One static binary for both roles: `platform serve` on the VPS and
  `platform deploy` etc. on your machine or in an agent's sandbox.
- The official Docker SDK uses the same types the Docker daemon uses.
- Small and predictable memory use, fast startup, no runtime to install.
- Go is simple enough that agent-written code stays readable.

Rust would save a few more MB but costs a lot of iteration speed. TypeScript
would let us share types with the UI natively, but costs RAM on the server and
needs either a runtime or a large compiled binary. The UI type-sharing problem
is solvable from Go (see below), so the server-side cost isn't worth it.

## Web UI

- **React + Vite + Tailwind**, TypeScript.
- Built to static files and **embedded in the Go binary** with `go:embed`. No
  Node process on the server. The UI costs nothing at idle.
- React Query for server state. Add Zustand only if real client state appears.

## API and type safety

- **Huma** (Go): define handlers with Go structs and get an OpenAPI 3.1 spec
  generated from them.
- **openapi-typescript + openapi-fetch** generate a typed client for the UI.
- The CLI calls the API with the same Go types.
- Side benefits: agents get a machine-readable spec and plain JSON over HTTP
  they can `curl`.
- **Live logs and deploy progress:** Server-Sent Events.

Alternative: ConnectRPC (protobuf schema, generated Go and TS clients, built-in
streaming). It gives a stricter contract but adds a schema language and codegen
step. See [open questions](open-questions.md).

## Storage

- **SQLite** in WAL mode, one file, backed up like any other file.
- `modernc.org/sqlite` (pure Go, no CGO) keeps the build a static binary.
- **sqlc** for typed queries, plain SQL migrations embedded in the binary.
- Secrets encrypted at rest (AES-GCM) with a key file on disk (`0600`), kept out
  of the database file.

## Reverse proxy and TLS

- **Caddy**, as a **separate process** (systemd), configured through its admin
  API on localhost. Runs with `--resume`, so it reloads the last config on its
  own. This means daemon restarts never touch routing.
- Automatic HTTPS via Let's Encrypt.
- Why not embed Caddy in our binary: then upgrading the daemon would restart the
  proxy and drop traffic.
- Worth a spike: **kamal-proxy**, a small Go proxy built specifically for
  zero-downtime container cutover with health checks and automatic TLS.

## Builds

- Docker Engine with **BuildKit** builds from a `Dockerfile`.
- **Railpack** (Railway's open-source builder) when there is no Dockerfile.
- Builds are the biggest resource risk on a small VPS. Allow one concurrent build
  and cap its resources. Building in CI and pushing an image is an option later.

## Rejected

- **Kubernetes / k3s:** hundreds of MB of overhead and a lot of moving parts for
  one server.
- **Docker Swarm / Nomad:** an extra layer on top of what the Docker API already
  does on a single host.
- **Postgres for platform state:** another service to run and back up, for data
  that fits easily in SQLite.

## Resource budget (target)

| Component | Target idle RAM |
|---|---|
| Docker Engine | Already needed by the apps |
| Caddy | ≤ 40 MB |
| platform daemon | ≤ 30 MB |
| **Platform overhead total** | **≤ 70 MB, ~0% idle CPU** |

The daemon should do no polling at idle. It reacts to API calls and Docker
events.
