# Experience

Both the CLI and the UI are first-class. Neither is "the real one" with the
other bolted on. Parity comes for free because both are thin clients of the
same API (see [architecture](architecture.md)).

## CLI

Users: developers in a terminal, agents in a sandbox, and CI.

- **Noun-verb commands** that read naturally: `platform service list`,
  `platform env set KEY=value`, `platform release rollback`. Common ones get
  short top-level aliases: `platform deploy`, `platform logs`, `platform status`.
- **Linked directories.** `platform link` remembers project, environment and
  service for the current directory, so most commands need no flags.
- **Contexts** for multiple servers (`platform context use prod-vps`), like
  kubectl. Also the hook for a future hosted control plane.
- **Machine-readable everywhere.** Every command supports `--json`. Output is
  stable and documented, so agents and scripts never parse human text.
- **Non-interactive when not a TTY.** No prompts in CI or agent sandboxes;
  missing input is an error with the exact flag to pass.
- **Errors say what to do next.** "Health check failed on `/healthz` after 30s.
  Previous release is still serving. See logs: `platform logs --release 42`."
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
$ platform domain add shop.example.com
Added shop.example.com → web (production)
Point DNS:  A  shop.example.com  →  203.0.113.10
Waiting for DNS... ok. Issuing certificate... ok.
https://shop.example.com is live.
```

- Any hostname, any number per service, added or removed at any time.
- `platform domain generate` gives a service a hostname under the base domain
  instantly, no DNS work needed.
- The UI shows each domain's state (waiting for DNS, issuing certificate,
  active, error) with the exact DNS record to create.

## Servers and promotion

```
$ platform server list
NAME      ROLE            BUILDER  STATUS   ENVIRONMENTS
hetzner   production      no       online   12
homelab   non-production  yes      online   31

$ platform promote staging production
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
