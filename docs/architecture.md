# Architecture

## Overview

```
 laptop / agent                         VPS
┌──────────────┐               ┌──────────────────────────────────────┐
│ platform CLI │──HTTPS+token─▶│ platform serve (one Go binary)        │
└──────────────┘   (Tailscale) │  ├─ HTTP API (OpenAPI)                │
┌──────────────┐               │  ├─ embedded web UI                   │
│   browser    │──────────────▶│  ├─ SQLite (state, audit log)         │
└──────────────┘               │  ├─ Docker SDK ──▶ Docker Engine       │
                               │  └─ Caddy admin API ──▶ Caddy :80/:443 │
                               └──────────────────────────────────────┘
```

- The daemon is the only thing that talks to Docker and Caddy.
- The CLI and UI only talk to the daemon's API.
- Agents use the CLI (or the API directly) with a scoped token.

## Data model

```
Project
 └─ Environment            production, staging, ...
     ├─ Variable           environment-level, shared by all services
     └─ Service            one container image (from repo, Dockerfile or image)
         ├─ Variable       service-level, overrides environment-level
         ├─ Domain         public hostname → service port
         ├─ Volume         named Docker volume, mount path
         └─ Release        immutable snapshot, one per deploy or config change
             └─ Event      build/deploy progress, status changes

Token        hashed, with scopes and an optional project restriction
AuditEvent   who did what, when, with which token
```

- A **Release** stores the image ID and the fully resolved config (variables,
  ports, limits, mounts). Rolling back means starting an old release as-is.
- **Variable references** like `${{postgres.DATABASE_URL}}` are resolved when a
  release is created, so each release is self-contained.

## Deploy flow

1. Client uploads source (tarball) or names an image.
2. Daemon takes the per-service lock, creates a Release in `building` state.
3. Build with BuildKit (or pull the image). Build logs stream to the client.
4. Start the new container next to the old one, on the environment's network.
5. Health check (HTTP path or TCP port) until it passes or times out.
6. **Pass:** switch the Caddy route to the new container, drain and stop the old
   one, mark the Release `active`.
   **Fail:** stop the new container, keep the old one serving, mark the Release
   `failed` with logs.
7. Write an AuditEvent. Release the lock.

## Networking

- Each (project, environment) gets its own Docker network. Services reach each
  other by service name, like Railway's private networking.
- Services with a domain publish their port on `127.0.0.1` only. Caddy proxies
  to that port, so nothing is directly exposed to the internet.
- Firewall: only 22, 80 and 443 open publicly. The daemon API listens on the
  Tailscale interface (see [open questions](open-questions.md)).

## Docker as the runtime source of truth

- Every container gets labels: `platform.project`, `platform.env`,
  `platform.service`, `platform.release`.
- Containers run with `restart: unless-stopped`, so they survive daemon and
  host restarts without the daemon's help.
- On startup the daemon **reconciles**: it compares SQLite (desired state)
  with Docker labels (actual state), reports drift, and never destroys anything
  it doesn't recognize.

## Process layout on the VPS

| Process | Managed by | Restarting it affects |
|---|---|---|
| Docker Engine | systemd | Everything (rare) |
| Caddy | systemd, `caddy run --resume` | Traffic, briefly |
| platform daemon | systemd | Only the API and UI |
| App containers | Docker restart policy | That app |
