# 0010: Environments are placed on servers

Status: accepted; superseded in part by [0015](0015-server-pools-and-placement.md) (placement: servers accept kinds instead of one role)

## Context

Production runs on a Hetzner VPS where every MB counts. Staging, dev and
preview environments, and all builds, don't need to live there. A homelab can
host them for free. We want one platform, one UI and one CLI across both, not
two separate installs.

The two servers play different roles. Hetzner is the stable one: it should
just keep running. The homelab is flexible: it gets rebooted, rebuilt,
reshuffled and experimented on, and it sits behind a home internet connection
and power. That flexibility is why staging, dev and previews belong there, and
also why production must never rely on it.

## Decision

### One control plane, many nodes

```
 Hetzner (production)                       Homelab
┌──────────────────────────────┐         ┌──────────────────────────────┐
│ dock serve                   │◀────────│ dock node                    │
│  ├─ control plane (API, UI,  │ outbound│  ├─ node runtime             │
│  │   SQLite, registry)       │ conn.   │  ├─ Caddy                    │
│  └─ node runtime (local)     │         │  ├─ BuildKit (builder)       │
│ Caddy                        │         │  └─ staging, dev, previews   │
│ production environments      │         └──────────────────────────────┘
└──────────────────────────────┘
```

- The **control plane** (API, UI, SQLite, scheduling) runs once, on the
  production server. It's small, and production is the most reliable and
  reachable machine.
- Every server runs a **node runtime**: Docker, Caddy, and optionally BuildKit.
  On the control plane's server it's in-process; elsewhere it's the same binary
  in `dock node` mode. This is the seam [0008](0008-keep-doors-open.md)
  prepared.
- **Nodes dial out to the control plane** over one long-lived authenticated
  connection and receive commands on it. The homelab needs no port forwarding
  for this, and the same model works for customer-owned servers later.
- A node joins with a one-time token: `dock node join <token>`.

### Hetzner never depends on the homelab

Dependencies only point one way: the homelab may depend on Hetzner, never the
reverse. Nothing that production needs to run, serve, deploy or roll back may
live only on the homelab. The control plane, registry and production data stay
on Hetzner, and the homelab can disappear at any time without production
noticing. Anything that would break this rule belongs on Hetzner or in CI
instead.

### Placement

Refined by [0015](0015-server-pools-and-placement.md): servers accept
production and/or non-production environments, there can be several of each,
and a project stays on one server per kind.

- **An environment lives entirely on one server.** All its services, volumes
  and its private network are on that server. No cross-server networking needed.
- Each server has a role: `production` or `non-production`.
- New environments go to the default non-production server. Environments named
  `production` go to the production server. Any environment can be pinned
  explicitly.
- Moving an environment between servers is a later feature (easy for stateless
  services, needs data migration for volumes).

### Builds and promotion

- Builds run only on servers marked `builder` (the homelab by default).
- Built images are pushed to a small OCI registry next to the control plane.
  Nodes pull from it. Pushes go outbound from the homelab, so this works
  through NAT too.
- `dock promote staging production` creates a production release from the
  exact image that ran in staging, with production's own variables. Production
  never rebuilds; what was tested is what ships.
- CI images (`--image`) skip all of this.

## Failure behaviour

| What's down | Effect |
|---|---|
| Homelab | Staging/dev show "server offline". Production unaffected. |
| Control plane (Hetzner) | No deploys anywhere. Apps on both servers keep running and serving (principle 1). |
| Homelab during a production deploy | Production can still deploy CI images, promote images already in the registry, and roll back to cached images. Only new source builds wait. |

## Consequences

- Production only carries its own apps, the control plane and the registry. No
  builds, no staging databases, no preview environments.
- Multi-server becomes core: the node protocol, version compatibility between
  control plane and nodes, and per-server health all land in M3.
- Each server needs its own way to receive public traffic for its domains
  (Cloudflare Tunnel on the homelab, see [0013](0013-homelab-ingress-cloudflare-tunnel.md)).
- The registry needs pruning, like images on the servers.
- Images built on the homelab and promoted to production make the homelab part
  of production's supply chain. If that's not acceptable for an app, its
  production releases can come from CI images only.
