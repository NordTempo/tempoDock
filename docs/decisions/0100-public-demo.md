# 0100: A public demo

Status: accepted (asked for by Saile on 2026-11-24, decided by the
agent).

## Context

People who hear about Dock should be able to click through it before
they install it on a server. Screenshots don't show how it feels, and
an install takes a server. The demo has to be the real UI and API, cost
next to nothing on the server it shares, and be safe to leave open to
anyone: no visitor may lock out the next one, run anything real or make
the server send traffic to an address they typed in.

## Decision

The demo is `dock serve` on the fake runtime (`DOCK_FAKE_RUNTIME=1`, the
in-memory runtime `make dev` uses) in a container Dock runs like any
app, at demo.nordtempo.com.

| Topic | Decision |
|---|---|
| Turning it on | `[demo] user = "<email>"` in the daemon's config. `dock serve` refuses it without `DOCK_FAKE_RUNTIME=1`: the demo signs anyone in as an operator, so it never runs with real containers behind it |
| Signing in | Everyone shares one account, `[demo] user`, an operator and admin of the demo's org, so the console is part of the tour. `POST /api/v1/auth/demo` starts a session for it without a password; `GET /auth/methods` says `demo`, and the sign-in page calls it on its own, so a visitor opening any link lands on that page signed in. Its session counts as confirmed until it ends: nothing asks a visitor for a password they don't have |
| What it refuses | An API operation list (`demoRefused`, internal/api/demo.go), each with why, in one 403 sentence: the shared account (password, second factors, name, removing or demoting it, the org's slug, leaving), how everyone signs in (sign-in settings, required second factors, SSO providers, the access model), and everything that makes the daemon connect to an address a visitor names (alert channels, heartbeat, log drains, status page hooks, mail, Cloudflare, buckets and backups, the registry's storage, the GitHub App and git, site tracking checks, updates), and visitors' files: source uploads (visitors deploy images instead, which the fake runtime pretends to run) and importing a vulnerability database. The demo's config also caps uploads at 1 MB. The MCP endpoint is off: its tools reach the same work. Everything else works, on the fake runtime |
| Why a list, not a check in core | It is one place, read in one sitting, and covers the CLI and the UI alike (both use the API). Core stays free of demo cases |
| Seeding | `dock-demo` (cmd/dock-demo), the image's entrypoint: empty the data dir, `dock admin bootstrap`, start `dock serve`, seed a small company (Acme: a shop with release history, staged changes, domains, jobs, analytics and errors; a blog with a locked environment and an access request; a docs site; an archived project; members, tokens, a status page with an incident) through the API with apitypes values, then create the demo user last. Seeding takes about 1.3 s |
| Ready | The demo is ready once its user exists: `GET /api/v1/auth/demo` answers 503 until then, and is the service's health check. Refusals start then too, so the seed may do what visitors can't |
| Reset | Every hour `dock-demo` stops the daemon and starts a fresh one from the seed: about 2 s of downtime. It does so early when the data dir passes 1 GB (checked every minute): it lives on the disk the server's apps use. Visitors' sessions end with it; the sign-in page signs them in again on the page they were on. No Dock cron job, token or workflow is involved |
| The image | `ghcr.io/nordtempo/dock-demo` (deploy/demo/Dockerfile): the release's `dock-linux-*` and `dock-demo` on distroless static, published by the release workflow next to tempoBase's. It reads release checks from an empty directory and names an empty analytics store binary, so it downloads nothing. About 75 MB of memory |

## Consequences

- The demo shows a release, not main, and moves to the next one when its
  service is deployed again.
- The fake runtime is what visitors see: deploys succeed in a second,
  logs and visits come from the demo's files. Making the fake more
  lifelike (backups that report a size, traces) improves `make dev` and
  the demo together.
- A new operation that connects to a user-given address, or changes how
  people sign in, needs a line in `demoRefused`.
- A visitor sees what others did until the next reset; offensive names
  last at most an hour.
