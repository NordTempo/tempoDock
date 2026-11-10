# 0015: Server pools, placement and the admin dashboard

Status: accepted (refines the placement part of [0010](0010-environments-on-servers.md)); superseded in part by [0025](0025-platform-console.md) (the admin dashboard is the platform console), [0053](0053-moving-environments.md) (environments move; rule 2 is a default)

## Context

[0010](0010-environments-on-servers.md) gave each server one role
(`production` or `non-production`) and assumed one server of each. We want
to grow by adding servers: more production servers as projects grow, the
homelab for everything that isn't production, maybe a rented staging server
later. Admins need one place to manage servers and users and to see how
loaded each server is.

## Decision

### Environment kinds

Every environment is either **production** or **non-production**. An
environment named `production` is production by default; the kind can be set
explicitly when it's created. The kind never changes after creation.

### Servers accept kinds

Each server has two switches: accepts production, accepts non-production
(either or both). Typical setup:

| Server | Production | Non-production |
|---|---|---|
| Hetzner VPS (control plane) | yes | no |
| Homelab | no | yes |
| Second production VPS | yes | no |

Turning a switch off stops new placements on that server; environments
already there stay until moved.

### Placement rules

1. **An environment lives on one server** (unchanged from 0010).
2. **A project stays together per kind.** All of a project's production
   environments are on one server, and all its non-production environments
   are on one server (often a different one). A project is never split
   across servers within a kind.
3. **The first environment of a kind picks the server**, and the project
   keeps it. Among the online servers that accept the kind, it picks the one
   with the most free room: memory reserved by placed services (their
   limits, or the default limit) against the server's total memory. Ties go
   to the server with fewer projects.
4. **Admins can pin** a project's kind to a server when nothing is placed
   yet. Moving a placed project is a later feature (stateless services are
   easy; volumes need a data copy).
5. No eligible server online means the environment can't be created, with an
   error naming the kind and the servers that would accept it.

No HA: an environment never runs on two servers. Spreading one service over
several servers stays a non-goal for now; nothing here blocks it later.

### Admin dashboard

One admin-only area in the UI (and the same in the CLI):

- **Users:** create, disable, enable, delete, reset password; see sessions
  and tokens per user.
- **Tokens:** as today.
- **Servers:** list with status, version, kinds accepted, what's placed
  (projects and environments), and statistics. Add a server (M3: join
  token), change accepted kinds, pin a project, disable a server for new
  placements.
- **Statistics:** live CPU, memory and disk per server and per service,
  plus history: 1-minute points for 24 hours and 10-minute points for 7
  days, stored in SQLite and pruned automatically.

### Statistics collection

- Read from cgroup files and `/proc` directly (no Docker stats API), once a
  minute, by the node runtime on each server. That's a few small file reads
  per container per minute: the only periodic work the daemon does, and well
  within "idle means idle".
- Live view: while someone watches a server or service, sample every few
  seconds and stream over SSE; stop when nobody watches.

## Consequences

- Adding capacity is "join a server, tick production". New projects fill it
  automatically.
- A project's production environments share one server's fate. That's the
  price of not splitting projects; it keeps private networking local and
  volumes in one place.
- Placement needs each server's total memory and reserved memory, which the
  statistics work provides.
- 0010's single "role" becomes two switches. Existing servers migrate as:
  production role → accepts production; non-production role → accepts
  non-production.
