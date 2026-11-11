# 0014: Node protocol

Status: accepted; updates over the link by [0028](0028-updates-from-the-console.md)

## Context

[0010](0010-environments-on-servers.md) puts the control plane on Hetzner and
lets other servers (the homelab) join as nodes that dial out to it. M3 needs
the wire between them. What it carries is fixed already: `node.Runtime`
(`internal/node/runtime.go`), 15 methods in four shapes: plain calls, calls
with progress (`Pull`), streams (`Logs`, `Events`) and a full-duplex upload
(`Build`: build context up, build output down).

Constraints: nodes sit behind NAT; Hetzner never depends on the homelab;
permissive licenses only ([0008](0008-keep-doors-open.md)); the binary is
already 32 MB. A spike in `internal/node/remote` implements the
recommendation below end to end.

## Decision

| Topic | Decision |
|---|---|
| Transport | **Reverse HTTP/2 over mutually pinned TLS**, standard library only. The node dials; the control plane is the HTTP client; the node serves `node.Runtime` as an `http.Handler`. |
| Auth | SSH-style: both sides have an Ed25519 key and pin the other's fingerprint. A one-time join token carries the control plane's fingerprint and a secret. |
| Versions | ALPN `dock-node/N` for the major version, a `hello` call for the minor. Control plane upgrades first. |
| Registry | CNCF Distribution on Hetzner. The control plane mints a short-lived, single-repository token per call; nodes hold no registry credentials. |
| Failure | Calls to an offline server fail fast with `server_offline`. On every reconnect the control plane reconciles that server. |
| Ingress | Per server: `ingress` (a fact the node reports) and `base_domain` (set on the control plane). |

## 1. Transport

### Options

| Option | NAT, Tailscale | Cloudflare HTTP proxy | Streams and cancellation | Added deps | License | Verdict |
|---|---|---|---|---|---|---|
| **Reverse HTTP/2 over TLS** | yes | no (raw TLS) | native: body up, body down, per-stream flow control, RST_STREAM cancels | none: `crypto/tls` + `net/http` are already linked | BSD (Go) | **chosen** |
| gRPC bidi | yes | no | yes; `io.Reader`/`io.Writer` become chunk messages | none (BuildKit links grpc), but adds protoc codegen | Apache-2.0 | The control plane must call the node, so it's reverse gRPC: a custom listener hack or our own "command stream" protocol on top. More machinery, same result |
| WebSocket + mux (coder/websocket + smux) | yes | yes | byte streams only; RPC, errors, cancellation are ours | 2 modules | ISC, MIT | Pays for a property we don't need yet (see below) |
| yamux over TLS | yes | no | byte streams only | 1 module | MPL-2.0 | Rejected: license |
| SSH (`x/crypto/ssh`, reverse channels) | yes | no | channels with flow control; RPC on top is ours | none (x/crypto already in go.mod) | BSD | Close second: pinning is built in, but everything above the byte stream is ours |

Why reverse HTTP/2:

- Multiplexing, flow control, cancellation and keepalive PINGs are already
  in the binary and battle tested. We write handlers, not a protocol.
- Every `Runtime` call is an ordinary HTTP handler on the node, testable and
  debuggable with standard tools (`GODEBUG=http2debug=2`).
- With the roles reversed the node never listens, and the control plane never
  accepts a request from a node, only responses (see
  [compromised node](#compromised-node)).

**Cloudflare.** The control plane is `direct` on Hetzner
([0013](0013-homelab-ingress-cloudflare-tunnel.md)), so the node link never
crosses Cloudflare's HTTP proxy; NAT on the homelab side doesn't matter
because the node dials. If a future hosted control plane must sit behind an
HTTP-only proxy, the same TLS bytes can ride inside a WebSocket (TLS stays end
to end, so pins still hold): an adapter in `remote`, not a redesign.

### Layers

```
 node (dock node)                           control plane (dock serve)
 dial TCP ───────────────────────────────▶  [nodes] listen, default :7443
 TLS 1.3 client, pins CP key     ◀──────▶  TLS server, requires a node key, ALPN dock-node/1
 HTTP/2 server (net/http)        ◀───────  HTTP/2 client (http.Transport.NewClientConn)
 remote.Handler(node.Runtime)               remote.Client implements node.Runtime
```

- **Own port**, not the API listener: the API is plain HTTP on Tailscale, and
  the link needs its own TLS for pinning. Unknown keys fail inside the TLS
  handshake, so even a public port exposes nothing.
- **Keepalive:** HTTP/2 PING after 15 s of silence, close if unanswered for
  15 s more, on both ends. Half-open links (expired NAT entry) are detected in
  about 30 s.
- **Reconnect:** node backoff 1 s doubling to 30 s, jittered, reset after
  every successful session. The newest session for a server replaces any
  older one.

### Calls

`POST /v1/<op>`. The request body is one line of JSON arguments, followed by
raw bytes for `Build`. A non-200 response is an error that happened before
anything started. A 200 response is NDJSON frames:

```
{"log":"#5 [2/4] RUN npm ci\n"}                             progress (Pull, Build)
{"item":{"Time":"...","Stream":"stdout","Text":"..."}}      stream element (Logs, Events)
{"end":true,"result":"sha256:..."}                          success, ends the call
{"end":true,"error":{"code":"not_found","message":"..."}}   failure, ends the call
```

A body that stops without an `end` frame means the connection was lost.

| `node.Runtime` method | Shape | Notes |
|---|---|---|
| `HasImage`, `EnsureNetwork`, `EnsureVolume`, `RemoveVolume` (protocol 20, ADR 0056), `Start`, `Stop`, `Remove`, `Inspect`, `List`, `ApplyRoutes`, `CheckDomain` | unary | |
| `HealthCheck` | unary, long | Runs on the node, where the port is bound to loopback. Blocks up to `hc.Timeout`. |
| `Pull` | logs, then result | Docker's pull progress as `log` frames. |
| `Build` | upload + logs, then result | Full duplex: output streams back while the context is still uploading. |
| `Logs` | item stream | Headers are flushed when the stream opens, so "not found" is still a returned error. The channel closes at the end, on cancel, or on connection loss. |
| `Events` | item stream, resubscribed | `Client.Events` keeps one channel across reconnects, as the interface promises. Events during an outage are lost; core reconciles the server on reconnect instead. |

| Cancellation | Effect |
|---|---|
| Caller cancels `ctx` | Client resets the stream; the node handler's `ctx` ends; Docker or BuildKit stops. A stalled build upload is cut too (the spike found that `net/http` alone hangs there). |
| Connection drops | Every in-flight call returns an error wrapping `remote.ErrOffline`; every handler on the node is canceled. |
| Deadlines | Not sent over the wire; the caller's cancel covers them. |

- **Errors:** `{code, message}`. `errors.Is` maps `not_found` to
  `node.ErrNotFound`, plus `canceled` and `deadline_exceeded`. Core maps
  `ErrOffline` to the API code `server_offline` (HTTP 503, CLI exit 7).
- **Wire types:** the spike encodes `node` structs as they are. The real thing
  uses JSON-tagged wire structs inside `remote`, so renaming a Go field can't
  break an older node.
- **No image bytes on the link.** Nodes talk to the registry directly.

## 2. Auth

### Identities

| | Control plane | Node |
|---|---|---|
| Key | Ed25519, `<data_dir>/link.key` (0600), created on first `serve` | Ed25519, `<data_dir>/node.key` (0600), created by `dock node join` |
| Pins | `servers.key_fingerprint`, one per node | control plane fingerprint in `/etc/dock/node.toml` |

A fingerprint is `sha256:` plus the hex SHA-256 of the public key (SPKI).
Fingerprints are public. Self-signed certificates only carry the keys: no CA,
no expiry, nothing to renew. This is `known_hosts` plus `authorized_keys`.

### Join

```
admin  dock server join homelab --role non-production --builder
       → join_tokens row: hash(secret), name, role, builder, expires in 1 h, single use
       → prints: dock node join dockjoin1_<base64url(addr, CP fingerprint, 32-byte secret)>

node   dock node join <token>
       1. creates its key
       2. dials addr; TLS pins the CP fingerprint from the token
          (an impostor fails here, before the secret is ever sent)
       3. CP sees an unknown key: allows the session in joining mode,
          where hello is the only call it makes
       4. node answers hello with Info plus the join secret
       5. CP checks hash, expiry and single use; stores the node's fingerprint
          on a new servers row; marks the token used; closes
       6. node writes node.toml, enables dock-node.service, reconnects as a known key
```

Joining-mode sessions are rate limited per source address and closed after
10 s.

### Rotation and revocation

| Action | How |
|---|---|
| Rotate a node key | `dock server rotate-key homelab`: CP calls `rotate`; the node creates a new key and returns its fingerprint; CP stores it as `next_key_fingerprint`; the node reconnects with it; CP promotes it on hello and forgets the old one. |
| Rotate the CP key | CP calls `trust <new fingerprint>` on every node (they accept both), switches keys, then `untrust` the old one. Nodes offline during this need to re-join. Rare. |
| Revoke | `dock server revoke homelab`: clears the fingerprint and closes the live session. The next handshake fails with a TLS alert; the node logs "revoked, re-join" and keeps retrying at the slowest backoff. |
| Remove | Only when no environments are placed on the server. |

### Compromised node

| Can | Cannot |
|---|---|
| Anything on its own machine, including the variables and volumes of every environment placed there (staging secrets) | Send requests to the control plane: on the link it is only ever an HTTP server, and it holds no API token |
| Lie in responses: health checks, logs, events, container state and domain checks for its own environments | See other servers' environments or secrets: the CP only sends values for environments placed on that server |
| (Builder) push a tampered image for a build it was asked to run, which promote could carry to production ([0010](0010-environments-on-servers.md)) | Impersonate another node or the CP (pinned keys) |
| Pull images it was handed tokens for | Push to any other repository, or anything once the 15 min token expires; deploy, promote, or change routes elsewhere |

Mitigations: promote by digest (a later push to the same tag changes nothing);
apps that require it take production images from CI only; the CP caps frame
and response sizes so a hostile node can't exhaust its memory.

## 3. Version compatibility

| Layer | Rule |
|---|---|
| ALPN `dock-node/N` | A breaking change bumps N. The CP accepts N and N-1 for one release cycle. Otherwise TLS fails with "no application protocol" and the node logs "upgrade dock". |
| `hello` protocol minor | Additive only: new ops, new optional fields. The CP checks it before calling a newer op; an unknown op is a 404. |
| Binary version | Reported in `hello`, shown in `dock server list`. Upgrade order: control plane first, then nodes. |
| Outside the window | Status `incompatible`: the link stays up for hello only, and nothing is placed or deployed there. |

Later, not M3: `dock server upgrade homelab`, where the node fetches the
control plane's own binary over the link and restarts.

## 4. Registry and promotion

**Where.** Distribution v3 (`registry:3`, Apache-2.0) on Hetzner, installed and
capped like BuildKit (never deployed through the platform), bound to
`127.0.0.1:5000` and published by Hetzner's Caddy as `registry.<cp-domain>`.
Storage is a local volume, or an S3-compatible bucket since
[0034](0034-registry-object-storage.md).

**Auth.** Distribution's `token` auth with the control plane as the issuer
(its signing key lives in the data dir). No long-lived registry credentials
exist on any node: for each call the CP mints a token scoped to one repository
and one action (`pull` or `push`), valid 15 minutes, and passes it in the call
arguments. Docker receives it as `RegistryAuth` (`registrytoken`) on pull;
BuildKit gets it through its session auth provider on push.

**Names.** `registry.<cp-domain>/<project>/<service>:<env>-<release>`.
Releases record the manifest digest; every pull on another server, and every
promote, uses `ref@sha256:...`.

```
CP picks a builder (homelab) ──Build(context, tag, push token)──▶ BuildKit on homelab
BuildKit exports twice: push to the registry (outbound HTTPS) + load into local Docker
CP ◀── image ID + manifest digest
target is the builder:   Start directly, no pull
target is another server: Pull(ref@digest, pull token) from the registry on Hetzner
```

**Pruning.** A nightly job on the CP keeps every digest referenced by a
release inside the rollback window (`keep_releases` per service and
environment, plus every active release), deletes other manifests through the
registry API (`delete.enabled`), then runs `registry garbage-collect
--delete-untagged` with the registry briefly read-only. It skips a night when a
build or pull is in flight.

**`dock promote staging production [--service web,api]`:**

1. Resolve staging's active releases (all services, or the listed ones). Each
   must have a registry digest; CI image references are used as they are.
2. Match services by name in production. Missing ones are listed and skipped.
3. Per service, one at a time: create a production release
   `{source: promote, image: ref@digest, from: staging #N}` with production's
   own variables and settings, then the normal deploy: pull by digest from the
   registry next to it, start, health check, cutover.
4. Stop at the first failure. Services already promoted stay (each passed its
   health check). Report per service.

Requires `deploy` on the production environment. Nothing on the homelab is
involved, so promote works with the homelab off.

## 5. Failure behaviour

**Server status** is runtime state, held by the hub like service status is
held by core: nothing polls. `last_seen_at` and `version` are persisted.

| Status | Meaning | Deploys |
|---|---|---|
| `online` | Connected, hello passed | yes |
| `offline` | No connection (since `last_seen_at`) | rejected: `server_offline` |
| `incompatible` | Connected, version outside the window | rejected: `version_incompatible` |
| `joining` | Join token issued, never connected | n/a |
| `revoked` | Key revoked | rejected |

The local server is always `online`. Changes publish a `server.status` SSE
event.

| Situation | Behaviour |
|---|---|
| Server offline when a deploy starts | Rejected before a release exists: 503 `server_offline`, CLI exit 7. |
| Link drops mid-deploy | The in-flight call returns `ErrOffline`; the release fails ("homelab went offline during health_checking"). The previous release keeps serving: it's a container on the node with a restart policy. The half-started container is removed on reconnect. |
| Link drops mid-build | The build fails; the node cancels BuildKit through `ctx`. |
| Interrupted stateful recreate | As after a daemon restart today: on reconnect, `ensureRunning` starts the previous release again. |
| Control plane restarts | All links drop; nodes are back within one backoff (at most 30 s). `recover` fails in-flight deploys at startup; their cleanup on remote servers waits for each node's reconnect. |
| Node reconnects, for any reason | `OnConnect`: per server, remove containers of failed releases, `reconcile` (statuses, drift), re-apply routes (the CP is the source of truth). `Events` resubscribes by itself. |
| Half-open link | PING timeout closes both ends in about 30 s; then as a drop. |
| Mutating call whose response was lost | Not retried (no idempotency keys); the reconnect reconcile sees the outcome and fixes it. |
| Caddy `ask` on a remote `direct` server | Answered by the node from its last applied routes, not by the CP, so certificates still issue while the CP is down. |

Concurrency moves from per control plane to per server: one build at a time
per builder (`buildSem` keyed by server).

## 6. Ingress and base domain per server

| Column | Owner | Meaning |
|---|---|---|
| `ingress` | node fact: set by the installer in `node.toml`, reported in hello, read-only on the CP | `direct` or `cloudflare_tunnel`. Decides the node's Caddy mode: on tunnel servers plain HTTP behind `cloudflared`, forwarding headers trusted from loopback only, a marker header for `CheckDomain`. |
| `base_domain` | CP setting: `dock server set homelab --base-domain up.example.dev` | Generated hostnames are `<service>-<project>[-<env>].<base_domain>` of the environment's server. |
| `public_ips` | node fact (direct only), from hello | What DNS should point at; used by domain checks and instructions. |
| `tunnel_hostname` | node fact (tunnel only), from hello | `<tunnel-id>.cfargotunnel.com`, the CNAME target shown for custom domains. |

`public_ip` and `base_domain` in `config.toml` seed the local server's row on
first start; after that the DB wins and the config keys are deprecated.

## 7. Data model, API and CLI

```sql
-- 0002_servers.sql (SQLite can't alter CHECKs, so servers is rebuilt)
servers:      + key_fingerprint TEXT UNIQUE      -- NULL for the local server
              + next_key_fingerprint TEXT
              + ingress TEXT NOT NULL DEFAULT 'direct'
                  CHECK (ingress IN ('direct', 'cloudflare_tunnel'))
              + base_domain TEXT NOT NULL DEFAULT ''
              + public_ips TEXT NOT NULL DEFAULT '[]'
              + tunnel_hostname TEXT NOT NULL DEFAULT ''
              + version TEXT NOT NULL DEFAULT ''
              + default_non_production BOOLEAN NOT NULL DEFAULT 0
              + revoked_at DATETIME
              - status                           -- runtime state, held by the hub
join_tokens:  id, hash UNIQUE, server_name, role, builder, created_by,
              created_at, expires_at, used_at
environments: + pinned BOOLEAN NOT NULL DEFAULT 0
releases:     + image_digest TEXT NOT NULL DEFAULT ''
              + built_on_server_id INTEGER REFERENCES servers (id)
              + promoted_from_release_id INTEGER REFERENCES releases (id)
```

**Placement** when an environment is created: `--server` pins it; else
`production` goes to the production server (an error asking for `--server`
if there are several); else the default non-production server; else the
production server (single-server installs keep working).

| API | |
|---|---|
| `GET /v1/servers` | status, version, last seen, role, builder, ingress, base domain, environment count |
| `POST /v1/servers/join-tokens` | admin; returns the token once |
| `PATCH /v1/servers/{name}` | admin; base domain, role, builder, default |
| `POST /v1/servers/{name}/revoke`, `/rotate-key` | admin |
| `DELETE /v1/servers/{name}` | admin; only with no environments |
| `POST /v1/projects/{p}/promote` | `{from, to, services[]}`; streams per-service progress like deploys |
| SSE `server.status` | status changes |

| CLI | Runs on |
|---|---|
| `dock server list` | anywhere |
| `dock server join <name> [--role] [--builder]` | anywhere (admin); prints the `dock node join` command |
| `dock server set\|revoke\|rotate-key\|remove <name>` | anywhere (admin) |
| `dock node join <token>` | the new server; writes key and `node.toml`, enables the unit |
| `dock node` | the new server; the daemon, run by systemd |
| `dock env create <name> [--server]` | anywhere |
| `dock promote <from> <to> [--service a,b]` | anywhere |

UI: a Servers page (status, version, last seen, ingress, base domain,
environments), a server badge on every environment, and a promote action
between environments.

`node.Runtime` changes: `Pull` takes a `PullRequest{Ref, Auth}`;
`BuildRequest` gains `Push{Ref, Auth}`; `Build` returns
`BuildResult{ImageID, Digest}`.

## 8. Implementation plan

Phase 0 is the spike (done). Phase 1 is one agent and short, because
everything else builds on it. Within a phase, tracks own disjoint paths.

| Phase | Track | Owns | Delivers |
|---|---|---|---|
| 1 | Contracts | `internal/node/runtime.go`, `internal/node/fake`, call sites in `internal/core` | The `node.Runtime` changes above, compiling everywhere with stubs; `apitypes` server and promote types; `server_offline` code |
| 2 | Link | `internal/node/remote/**` | Wire structs, joining mode, key files, `rotate`/`trust`, size limits, hello facts (ingress, IPs, tunnel hostname, builder, Docker version) |
| 2 | Store | `internal/store/**` | Migration 0002, sqlc queries |
| 2 | Registry and build | `internal/registry/**` (new: token minting, prune), `internal/node/build`, `internal/node/docker` | Push plus digest, pull by digest with a token |
| 2 | Proxy | `internal/node/caddy/**` | Tunnel mode, marker header, local `ask` from the last routes |
| 2 | Install | `deploy/**` | `--node` install, `cloudflared` unit, registry container, node port, `dock-node.service` |
| 3 | Core | `internal/core/**`, `internal/apitypes/**` | Servers (join, status, revoke, set), runtime per server through the hub, placement, builder routing, per-server build lock, reconnect reconcile, promote |
| 3 | Surface | `internal/api/**`, `internal/cli/**`, `internal/config/**`, `cmd/dock` | Endpoints, `dock server *`, `dock node join`, `dock node`, `dock promote`, `[nodes]` config, hub wiring in `serve` |
| 3 | Web | `web/**` | Servers page, environment badges, promote |
| 4 | Cutover | docs, real machines | Two-VM end to end test; move the CP to Hetzner; homelab joins; update architecture, safety model, CLI docs |

Surface and Web start from Core's agreed function signatures and the OpenAPI
spec, so they can begin with stubs early in phase 3.

## Spike results

`internal/node/remote`: `Hub` (control plane), `Client` (`node.Runtime` over
the link), `Node` (dial loop) and `Handler`, all 15 methods. Tests run the
fake runtime over real loopback TCP and TLS through a proxy that can cut or
freeze connections.

| Check | Result |
|---|---|
| Unary calls, error mapping (`ErrNotFound`) | pass |
| `Logs` streaming, cancel ends the node-side stream | pass |
| `Build`: logs arrive while the context is still uploading; 8 MiB checksum matches; failure and cancel mid-upload | pass |
| Hard drop mid-call: `HealthCheck` returns `ErrOffline`, `Logs` closes, node reconnects, `Events` channel survives and delivers again | pass |
| Half-open link (traffic silently dropped) | detected in 2x keepalive (600 ms at 300 ms) |
| Revoked node stays out, then returns when un-revoked; node refuses an impostor control plane | pass |
| One log line, node runtime to CP channel, loopback | p50 44 µs, p99 91 µs |
| Build context upload, loopback | about 1.1 GB/s |
| Binary size (`-s -w`, both ends linked) | +248 KB (0.8 %), no new modules |

Not in the spike: joining mode and key files, wire structs, size limits,
hello facts, `rotate`/`trust`, rate limiting, `ErrOffline` mapping in core,
integration with the real Docker runtime.

## Consequences

- No new dependencies; the protocol is HTTP semantics we already know.
- Remote servers become a `node.Runtime` like the local one; core changes are
  about which server, not how.
- The node exposes nothing to the network, and the control plane accepts
  nothing from nodes but answers.
- One more port on the control plane, one more container (the registry).
- A compromised builder can still poison promoted images; CI-only production
  images remain the answer for apps where that matters.
- Events during an outage are lost by design; correctness rests on the
  reconnect reconcile.

## Resolved questions

Decided 2026-10-21.

| Question | Decision |
|---|---|
| Node port `:7443` exposure | Tailscale only. Public stays possible later (unknown keys fail in the handshake) for nodes off the tailnet. |
| Registry exposure | Tailscale only. CI images keep coming from external registries (`--image`). |
| Deploy to an offline server | Rejected immediately with "server X is offline". No queue. |
| Promote with services missing in production | Skipped and listed in the output. Production services are only created on purpose. |
| Control plane key rotation | Not built in M3. Re-joining nodes is the documented procedure. |

## Amendment (2026-10-22): registry over the node link

Decided by the agent while integrating M3, replacing "registry reachable over
Tailscale" for nodes.

- The registry container listens on `127.0.0.1:5000` on the control plane
  and is never exposed on any network.
- Every node runs a small registry proxy on its own `127.0.0.1:5000` that
  forwards over the existing node link (one more stream type, like Dial).
  Pulls and builder pushes go through it; Docker treats loopback registries
  as plain HTTP without extra config.
- Image references are therefore identical on every server
  (`127.0.0.1:5000/<repo>@sha256:...`), and nodes never need registry
  credentials of their own: the control plane mints a short-lived
  per-repository token for each pull or push, as before.
- No extra port, TLS setup or Tailscale dependency for images. A node that
  can reach the control plane can pull.
- `dock server add <name> [--production] [--non-production] [--builder]`
  creates the join token and prints the exact install command for the new
  server; `install.sh --node` runs `dock node join`, which stores the link
  state in `/var/lib/dock/node.toml`.
- `dock admin caddy-config` must work before a node has joined: in tunnel
  mode the server marker is filled in on first connect (derived from the
  node key fingerprint), not at install time.

## Amendment (2026-10-22): hardening against a compromised node

A security review of the multi-server code found gaps between the
[compromised node](#compromised-node) table and the code. Decided by the
agent; each has a focused test.

| Gap | Now |
|---|---|
| A revoke during a node's hello (up to 10 s) found no session to close; the node registered afterwards and was online | The hub looks the key up again when registering, under the lock `Disconnect` takes. Core also refuses calls to a revoked server whatever the hub says. |
| A production build could run on any builder, which gets the environment's variables as build args and secrets | Production builds only run on builders that accept production environments. With none, the deploy fails and says to make a production server a builder or deploy a CI image. Non-production builds go to any builder. |
| A 1 MiB answer could decode into 300k containers; one drift audit row per stray container; a node could send `dock.resync` events to force reconciles | Lists in answers are capped at 10,000 items; stray containers are one drift entry per reconcile; resync events from the wire are dropped (only the client's own reconnect signal counts). |
| Node calls had no deadline, so one hung node stalled the domain and stats loops for every server | Control calls time out after 30 s (Stop and HealthCheck after their own timeout plus 30 s, Pull after 30 min, Build per `[buildkit] timeout_minutes`, default 30). Logs and Events streams stay open; keepalive catches dead links. The domain and stats loops ask servers at once, each with its own time budget. |
| Build and pull output was unbounded | A release's log stops at 16 MB with a marker line; the build goes on. |
| Key rotation accepted any fingerprint the node named, including another server's key | A fingerprint that is any server's key or next key is refused (also on join). |
| Pull tokens were minted for any repository in the registry, and users could name registry images | Tokens only for the deploying project's repositories (its own, and promotion sources, which need read access). Image references under the registry host, or at a CGNAT/Tailscale address (100.64.0.0/10), are refused. |
| `GITHUB_TOKEN` and the join token were in process arguments | curl reads the header from a config on stdin; `dock node join --token-file -` reads the token from stdin. |

Still open: `dock server add` prints the install command with the join
token in an environment assignment on `sudo`'s command line. The token
works once and only for an hour, and the window ends when the node joins.

## Amendment (2026-10-30): images in object storage

[0034](0034-registry-object-storage.md) lets the registry keep its images
in an S3-compatible bucket (a storage connection) instead of the control
plane's disk, and moves them either way with `dock registry migrate`.
Pulls still go through the registry (`redirect: disable`), so the node
link's registry proxy and the per-call tokens above are unchanged.

## Amendment (2026-10-30): Cloudflare tunnels run by the control plane

[0036](0036-cloudflare-api.md) lets the control plane create (or adopt)
a tunnel server's Cloudflare tunnel. Protocol 8 adds `ensure-tunnel`:
the control plane sends the tunnel's token (or none, to stop it) on
every reconnect, and the node runs `cloudflared` in its
`dock-cloudflared` container. The token runs only that server's tunnel,
so nodes still hold no platform credential. Hello now also reports the
node's Caddy HTTP port (`http_port`), which the tunnel routes to, and the
control plane refuses a hello whose `tunnel_hostname` isn't a
`<uuid>.cfargotunnel.com` or whose `public_ips` aren't addresses DNS can
point at: section 6's facts end up in DNS. A tunnel the control plane
runs is the server's `tunnel_hostname`, whatever the node reports.
Protocol 9 lets a direct server run one next to its public IP: hello
reports the port its cloudflared forwards to (`tunnel_port`), and
routes may be `tunnel_only` (served on that listener alone).

## Amendment (2026-10-30): analytics over the link

[0037](0037-analytics.md) adds Dock Analytics in protocol 10. Every
server spools events from its own ingest; the control plane, still the
only caller, reads them with `tail-spool` (an item stream like `events`,
batches of up to 1,000 events with their spool position) and confirms
them with `ack-spool`. `set-analytics` hands a server the day's salts,
`ensure-analytics-store` runs the store's container on the analytics
server, and `dial-analytics` is a raw connection to the store's port
there, like `dial`, reaching nothing else. Routes gain analytics
hostnames with their tag; a node below protocol 10 gets its routes
without them, so its sites go uncounted until it is updated.

## Amendment (2026-11-10): image scanning on builders

[0074](0074-image-vulnerability-scanning.md) scans live images for
vulnerabilities on builders. Protocol 23 adds `scan-sbom` (Syft's SBOM
streamed down as log frames), `scan-match` (an SBOM up, Grype's findings
down as item frames, at most 5,000), `scan-database` and
`image-digest`. Each runs one job container that cancelling the call
kills, which is how a build or deploy preempts a scan. Nothing on the
node keeps state but the extracted Syft binary in its data dir and the
database volume.

Protocol 27 (2026-11-11; 24 to 26 are tracing's) adds `remote-digest` (which digest a registry
serves for a tag, asked through the node's Docker without pulling) and
`scan-database-import` (a database archive streamed up as the body and
imported by a job without network), and a build's result names the
image its final stage was built on (`base`, `base_digest`; older nodes
leave them out).
