# 0006: Every way to build and deploy

Status: accepted

## Context

Builds are the single biggest resource risk on a small server. A Node or
Next.js build can take 1-2 GB of RAM and saturate the CPU. Different apps and
workflows also want different paths: a quick deploy from a laptop or agent, a
deploy on every push, or a CI pipeline that already produces images.

## Decision

Support all of them through one release model. A release is created from
either **source** or an **image reference**; everything after that is identical.

| Path | Trigger | Where the image is built |
|---|---|---|
| Source upload | `platform deploy` from a laptop or agent | A builder server |
| Git push | GitHub webhook (M4) | A builder server |
| Prebuilt image | `platform deploy --image ghcr.io/...` from CI | CI; servers only pull |
| Promotion | `platform promote staging production` | Nowhere: reuses the image already built ([0010](0010-environments-on-servers.md)) |

- **Builder servers** are marked as such ([0010](0010-environments-on-servers.md)).
  By default the homelab builds and production doesn't, so production never
  spends CPU or RAM on builds.
- Builds run in a dedicated BuildKit container with hard CPU and memory limits
  and low priority, one at a time per builder. A build that exceeds its cap
  fails; running apps are unaffected.
- Railpack is used when there is no Dockerfile.
- Built images are pushed to the platform's registry so any server can pull
  them (see [0010](0010-environments-on-servers.md)).

## Consequences

- Day one is as easy as "push source", and heavy apps can move to CI without
  changing anything else.
- Production servers can run with no build tooling load at all.
- We need a registry once there is more than one server.
