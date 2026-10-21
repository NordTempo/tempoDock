# Experience

Both the CLI and the UI are first-class. Neither is "the real one" with the
other bolted on. Parity comes for free because both are thin clients of the
same API (see [architecture](architecture.md)).

## CLI

Users: developers in a terminal, agents in a sandbox, and CI.

- **Noun-verb commands** that read naturally: `dock service list`,
  `dock env set KEY=value`, `dock release rollback`. Common ones get
  short top-level aliases: `dock deploy`, `dock logs`, `dock status`.
- **Linked directories.** `dock link` remembers project, environment and
  service for the current directory, so most commands need no flags.
- **Contexts** for multiple servers (`dock context use prod-vps`), like
  kubectl. Also the hook for a future hosted control plane.
- **Machine-readable everywhere.** Every command supports `--json`. Output is
  stable and documented, so agents and scripts never parse human text.
- **Non-interactive when not a TTY.** No prompts in CI or agent sandboxes;
  missing input is an error with the exact flag to pass.
- **Errors say what to do next.** "Health check failed on `/healthz` after 30s.
  Previous release is still serving. See logs: `dock logs --release 42`."
- **Meaningful exit codes**, so agents can branch on outcomes (failed health
  check vs auth error vs not found).
- **Live progress** for deploys: build, start, health check, cutover, streamed
  as they happen.
- **Fast.** One static binary, no runtime start-up cost.

## UI

Users: developers who want an overview, and anyone who prefers clicking.

- **Overview first.** Landing on a project shows every service in every
  environment with status, current release, domain and resource use at a
  glance. Problems surface at the top.
- **Information dense.** Dark, true black background, white text, no
  decorative cards or chrome. Built for scanning, not for screenshots.
- **Live, without polling.** Status, deploy progress and logs update over
  Server-Sent Events.
- **Everything has a URL.** Every project, service, release and log view is
  linkable and survives a reload.
- **Keyboard first.** Command palette (`Cmd+K`) for navigation and actions.
- **Variables editor** with environment vs service scope, references, bulk
  paste of `.env` files, and secret values hidden by default.
- **Diffs before risky changes.** Changing domains, volumes or limits shows
  what will happen before it happens.
- **No continuously repainting animations** (spinners, shimmer, pulse). Static
  state indicators instead.

## Domains

The flow is the same in both clients:

```
$ dock domain add shop.example.com
Added shop.example.com → web (production)
Point DNS:  A  shop.example.com  →  203.0.113.10
Waiting for DNS... ok. Issuing certificate... ok.
https://shop.example.com is live.
```

- Any hostname, any number per service, added or removed at any time.
- `dock domain generate` gives a service a hostname under the base domain
  instantly, no DNS work needed.
- The UI shows each domain's state (waiting for DNS, issuing certificate,
  active, error) with the exact DNS record to create.

## Servers and promotion

```
$ dock server list
NAME      ROLE            BUILDER  STATUS   ENVIRONMENTS
hetzner   production      no       online   12
homelab   non-production  yes      online   31

$ dock promote staging production
Promoting web@staging (release 57, sha256:4be1…) to production on hetzner
Health check... ok. Traffic switched. https://shop.example.com
```

- Where things run is visible everywhere: every environment shows its server.
- Placement is automatic; pinning an environment to a server is one command or
  one dropdown.

## Shared

- **Same words everywhere.** Project, environment, service, release, variable,
  domain, volume. The CLI, UI, API and docs use identical terms.
- **Same permissions everywhere.** A token or user can do the same things from
  either client. Permission errors explain which scope is missing.
- **Audit log visible from both.**

## Performance targets

Verified in M0 and tracked as we go.

| What | Target |
|---|---|
| Daemon idle RAM | ≤ 30 MB |
| Daemon idle CPU | ~0% (no polling) |
| CLI command start-up | ≤ 50 ms before network |
| API read latency on the server | p95 ≤ 20 ms |
| UI initial JS (gzipped) | ≤ 200 KB |
| Rollback to a cached image | ≤ 10 s to traffic switch, after health check |

## Measured (M0)

On `dock-lab`, a homelab VM (Debian 13, 4 vCPU, 4 GB RAM, Docker 29), unless
noted. "Private" is anonymous memory (what the process really costs); RSS also
counts mapped binaries.

| What | Measured | Notes |
|---|---|---|
| Caddy idle | 12.8 MB private, 0% CPU | 53 MB RSS with 50 routes loaded |
| BuildKit idle | 13-15 MB, 0% CPU | Capped container, `CPUShares=256` |
| Registry idle | 15 MB, 0% CPU | `registry:3` |
| BuildKit peak, Next.js build | ~410 MB | Cold cache, 42 s, 1 CPU cap (dev VM) |
| Build over its memory cap | Fails in 3 s, clear error | BuildKit keeps running |
| App latency during a build | p99 1.2 ms idle, 1.3 ms during | 0 failed requests (dev VM) |
| Caddy config reload | p50 2-3 ms, p95 3-18 ms | Full config, ~50 routes |
| Requests during 50 route swaps | 0.01% of new connections dropped, 0 wrong responses | Upstream Go limitation, see `internal/node/caddy` |
| CLI start-up | 2 ms | `dock context list` |
| UI initial JS | 109 KB gzipped | Tooling scaffold only |
| Daemon idle | not yet measured | Needs the wired daemon |

Known limits found in the spike:

- Caddy reloads drop a handful of brand-new connections and can leave HTTP/2
  connections on the old route until its 10 s grace period ends. Cutovers
  therefore drain the old container for longer than that before stopping it.
- Railpack downloads a 150 MB version-resolver binary to `/tmp/railpack` on
  the first zero-config build.
