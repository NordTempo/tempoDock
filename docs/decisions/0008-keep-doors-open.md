# 0008: Keep open source and hosting possible, cheaply

Status: accepted

## Context

We build for NordTempo first. Open source and paid hosting may follow. Building
for them now (organizations, billing, multi-tenant isolation) would slow us down
for a future that may not happen. Some choices, though, are cheap now and very
expensive to retrofit.

## Decision

Pay only for the cheap ones:

1. **Control plane / node runtime boundary.** Core logic talks to Docker, builds
   and the proxy through a Go interface (`internal/node`). In-process for the
   local server, over a connection for remote ones
   ([0010](0010-environments-on-servers.md)). A hosted control plane would
   split at the same place.
2. **A `Server` entity in the data model** from day one.
3. **Users and roles exist**, even if v1 has one admin. Permissions go through
   one code path for users and tokens.
4. **Auth doesn't assume Tailscale.** Tailscale is a deployment choice, not
   something the code relies on.
5. **No NordTempo-specific assumptions in code.** Domains, registries and
   defaults are configuration.
6. **Permissive-licensed dependencies only** (MIT, Apache-2.0, BSD), so any
   license choice remains possible later.
7. **CLI contexts** (`dock context use ...`), so pointing the CLI at a
   different server or a hosted endpoint needs no redesign.

Explicitly not built now: organizations or teams, billing, quotas, running
untrusted workloads, multi-region.

## Consequences

- Small, bounded cost in v1 (an interface, a table, a role column).
- Moving to open source is mostly docs, install polish and a license.
- A hosted offering would most likely be "we run the control plane, customers
  bring servers", which the node boundary and contexts support. Hosting
  untrusted customer workloads on our servers would need a separate design.
