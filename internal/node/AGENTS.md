# internal/node

The boundary between the control plane and a server. Core only sees
`node.Runtime` (`runtime.go`); nothing outside this tree imports Docker,
BuildKit or Caddy.

```
runtime.go   the interface, specs, labels; read its doc comments first
local.go     Local: the control plane's own server, composed of the below
docker/      containers, networks, volumes, logs, events, health checks, jobs
build/       BuildKit and Railpack builds, image pushes
caddy/       renders the full Caddy config from routes, the TLS ask endpoint
stats/       /proc and cgroup v2 reads (Docker only to name a new container)
remote/      the node link (ADR 0014): Hub and Client on the control
             plane, Node on the server, wire types, join
fake/        in-memory Runtime for core tests and `make dev`
firewall/    iptables rules keeping apps and builds off private networks
```

## Changing the Runtime interface

A new or changed method has to land everywhere at once, or the build or
a remote server breaks:

1. The interface and its doc comment in `runtime.go`.
2. The real implementation (`docker/`, `build/`, `caddy/` or `stats/`).
3. `fake/fake.go`, faithful enough for core's tests to mean something.
4. `remote/`: an `op...` constant in `protocol.go`, request and result
   types in `wire.go`, the `Client` method in `client.go` and its handler
   in `node.go`.
5. `offlineRuntime` in `internal/core/placement.go`.

The remote protocol is versioned. Within `dock-node/1` changes are
additive only: new ops and new optional JSON fields. A new op bumps
`Protocol`, says so in its comment, and the `Client` calls `needProtocol`
first so an older node gets a clear error instead of a 404. Wire types are
separate from the `node` structs on purpose: JSON names are the contract,
durations travel as milliseconds.

## Rules

- Only touch resources labelled `dock.managed` for this control plane
  (`dock.control_plane`). Another install's containers, networks and
  volumes are ignored, never removed. Named volumes are never removed by
  the runtime, with one exception: `RemoveAnalyticsStore` removes the
  analytics store's own volume, which core calls only once the store's
  data was copied to its new server and checked, or the operator chose
  to start empty (ADR 0037). Anonymous volumes (an image's `VOLUME` that
  no Dock volume covers) go with their container: remove containers with
  `RemoveVolumes: true`, also in tests, or they pile up on the disk.
- No polling: follow Docker's event stream. Stats are the one periodic
  job and read files, not the Docker API (except once per new container,
  to name it in the memory breakdown).
- Stream logs, build output and image layers; never read them whole.
- Real-Docker tests are `integration_test.go` / `*_integration_test.go`
  behind `//go:build integration` (`make test-integration`).
