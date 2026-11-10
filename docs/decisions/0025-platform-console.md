# 0025: The platform console is Dock's admin panel

Status: accepted (decided by the agent, direction picked by the owner); superseded in part by [0027](0027-organizations.md) (item 2: operators), [0038](0038-own-tokens.md) (item 3: tokens), [0062](0062-org-alerts-and-operators.md) (the console's pages and overview)

## Context

The platform console (design board B1) held one page: servers. Everything
else an admin runs was in workspace settings next to the caller's own
tokens (members, tokens, alerts, data access, the audit log), backups had
an API and CLI but no UI, and the registry (ADR 0014 section 4) had
neither. The owner asked for "a bigger and better admin panel" with
servers, users and registry information, and picked option A of three
mocks: a grouped console sidebar with an overview first.

Board B2 split the console into infrastructure, tenants (workspaces, plans
and limits) and system (operators, updates), with an operator flag apart
from workspace roles. One Dock is one workspace, so tenants and a separate
operator flag have nothing to hold yet.

## Decision

1. **Everything platform-wide lives under `/console`**, for admins only:
   Overview; Infrastructure (Servers, Registry, Backups); Access (Users,
   Tokens, Data access); Operations (Alerts, Audit). A layout route
   (`_app.console.tsx`) sends anyone else to their projects; the API
   refuses them anyway. The shell gives every page under it the console
   sidebar and the amber bar. One prefix keeps the reserved top-level
   names (which shadow project names in `/$project`) to one.
2. **Admin scope is the operator.** No operator flag, no tenant sections.
   They slot in under the console if Dock ever hosts several workspaces.
3. **The workspace keeps what isn't platform-wide**: projects, Activity
   (`/audit`) for everyone, and Tokens (`/tokens`) for non-admins, who
   see their own. Admins' `/tokens` redirects to the console, where they
   see every token. Server pages are console pages, so non-admins see
   server names as text.
4. **The registry gets an API**: `GET /registry` (size on disk, the
   rollback window, the last prune's outcome since the daemon started,
   and each service with images: how many its window keeps, its newest and
   active release) and `POST /registry/prune` (prune and collect now,
   in the background; 409 while builds or pulls use the registry). CLI:
   `dock registry`, `dock registry prune`. The service list comes from
   one query over the releases, never from walking the registry's
   catalog (a HEAD per tag); the size is one walk of the blob store.
   Automatic pruning stays as it was (after deploys and archives, at most
   hourly).
5. **The overview composes existing reads** (servers, open alerts, backup
   settings, registry, users, audit) instead of a new aggregate endpoint.
   Each is already cached and kept live by events; no new polling.

## Consequences

- One place answers "is anything wrong with Dock" and "who has access".
- URLs moved (`/servers` is `/console/servers`, `/users` is
  `/console/users`, ...). Pre-launch, no redirects were kept.
- Registry prune state resets when the daemon restarts; the page says
  "not since start" until the next prune.
- The overview makes six requests where one aggregate could make one.
  They run in parallel on an admin page and each stays small; if it ever
  shows in `make perf`, an aggregate endpoint replaces them.
- Not done yet: a user's "last seen" (sessions don't record use, and
  recording it means a write per request) and a read-only System page
  with the config in effect.
