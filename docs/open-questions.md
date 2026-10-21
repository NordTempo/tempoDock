# Open questions

Each question has a current leaning. Move answered ones into the relevant doc.

## Name

`platform` is a placeholder. Needed before the CLI binary name gets baked in.

## Language

Leaning **Go** for daemon + CLI, TypeScript for the UI only. See
[tech stack](tech-stack.md). Confirm after M0.

## API style: Huma (OpenAPI) or ConnectRPC?

Leaning **Huma**. Plain JSON over HTTP is easy for agents to `curl`, and the
OpenAPI spec doubles as docs. ConnectRPC gives a stricter schema and streaming,
but adds protobuf and a codegen step.

## Proxy: Caddy or kamal-proxy?

Leaning **Caddy** for flexibility (wildcards, on-demand TLS, static sites). kamal-proxy
does health-gated cutover itself, which could save code. Decide in M0.

## Where do builds run?

Leaning **on the VPS** for simplicity, one build at a time with resource caps.
Building in CI or on the laptop and pushing an image avoids build spikes on the
server. Could support both: `deploy` from source or `deploy --image`.

## Source of truth for config: database or repo file?

Railway keeps most config in the platform and optionally reads `railway.json`.
Leaning **database as source of truth** (so the UI can edit everything), with an
optional repo file for build settings (Dockerfile path, health check, start
command) that is applied on deploy.

## UI access

Leaning **Tailscale-only** for the API and UI at first, plus an admin login.
Public access with proper auth (better-auth or similar) later if needed.

## Image registry

Not needed while builds happen on the VPS. Revisit if we add CI builds or
multiple servers.
