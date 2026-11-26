# 0089: Faster builds: more cores, zstd layers, pinned Railpack images, cold previews

Status: accepted (asked by Saile through the coordinator, 2026-11-17).
Amends [0006](0006-every-way-to-build.md): the builder's CPU cap.
Amended the same day: BuildKit pushes, and builders fetch Railpack's
images ahead (below); that amends
[0084](0084-registry-storage-gateway.md)'s reads.

## Context

Builds are what people wait on most. A one-line change to a Next.js 16 site
(detoo, on prod) took about two minutes, 78 s of it `next build`:
Turbopack compiling the whole app again "using 1 worker", then 31 s of
TypeScript. Measured on the dev VM with Dock's own builder, its own
BuildKit and a registry on loopback (`TestBuildTimes`,
`e2e/apps/nextjs-pnpm`: pnpm, Next.js 16.4, Tailwind 4, 44 pages,
standalone output, the Next.js example Dockerfile), with the old
defaults (one core, 2 GB):

| Step | Dockerfile, cold | Dockerfile, warm | Railpack, cold | Railpack, warm |
|---|---|---|---|---|
| Source pack and upload | 2 ms (13 KB) | 2 ms | 2 ms | 2 ms |
| Pulling or resolving Railpack's images | | | ~20 s | 1 to 15 s |
| Dependency install | 13.4 s | cached | 17.1 s, after 9 s of apt and mise | cached |
| `next build` | 20.6 s | 20.9 s | 18.5 s | 8.1 s (its cache mount) |
| Export into Docker | 3.5 s | 4.1 s | 26.0 s | 4.5 s |
| Push to the registry | 0.4 s | 0.3 s | 0.7 s | 0.2 s |
| Start to the first 200 | 0.8 s | 1.1 s | 1.2 s | 0.8 s |
| **Build and push** | **59.8 s** | **35.4 s** | **106.5 s** | **35.3 s** |

What stands out:

- **The builder had one core.** `[buildkit] cpus` defaulted to 1, and
  Turbopack, TypeScript, pnpm and layer compression scale with cores: a
  warm Dockerfile build takes 35.4 s on one core, 23.2 s on two, 20.2 s
  on four. Apps were already protected without the cap: the BuildKit
  container runs at a quarter of the normal CPU weight, so an app that
  wants the CPU gets it.
- **Dockerfile builds of Next.js apps keep no compiler cache.** The
  layer cache works (the install step is `CACHED` when the lockfile is
  unchanged), but the step that runs `next build` copies changed source,
  so it runs again, and without a cache mount on `.next/cache` Turbopack
  starts from nothing: 9.3 s to compile against 1.0 s with the cache.
  Railpack builds already mount it.
- **Railpack resolved its images on every build.** Its frontend,
  builder and runtime images are named by tag, so each build asked
  ghcr.io what they point at: under a second on a good day, 15 s of a
  35 s warm build on a bad one, and a failed build when ghcr.io is down.
- **Layers were gzipped,** single-threaded per layer: 26 s of a cold
  Railpack build (its image carries node_modules and mise).
- **Upload, push, start and health check are small** next to the build.

## Decision

**`[buildkit] cpus = 0` is the default: half the server's cores, but at
least two (all of a smaller server).** The memory cap stays the hard
limit; the CPU weight stays a quarter of an app's. At least two because
one core to two is most of the gain and small servers are the common
case (detoo's server runs Next.js with one worker); half on larger ones
so a build can't take the whole server when something else (the daemon,
a backup, a scan) starts. `install.sh` writes `cpus = 0`. An existing
config keeps what it says, which for installs before this is `cpus = 1`:
the release notes say to change it.

**Layers are exported with zstd.** About three times faster than gzip
(the cold Railpack export 15.6 s to 5.2 s on two cores).
Docker reads zstd layers since 23 (Dock needs 28). Layers that already
have a blob keep it, base images' and earlier builds' gzip ones
included, so an upgrade doesn't push every layer again.

**Railpack's images are pinned by digest** (`railpackImages`), through
a BuildKit source policy for the images its plans name and the digest in
the frontend reference. The tags are versioned with Railpack, so the
pins change when Railpack does, and a test fails until they are updated.
The `alpine:latest` Railpack hashes secrets in is pinned too: a helper,
nothing of it reaches the image.

**Dock says when a Dockerfile keeps no `.next/cache`.** When the
source's package.json depends on `next` and no `RUN` step mounts a cache
on `.next/cache`, the build log gets one hint line with the mount to add,
targeted at the `WORKDIR` of the step that builds. Dock doesn't add the
mount itself: a cache mount isn't part of the image, so a Dockerfile that
copies `.next` into the runtime stage would lose what Next.js wrote to
`.next/cache` at build time (its fetch cache), silently changing what the
user wrote. The guide's "Build caches" section lists the mounts for
Next.js, pnpm, npm, Yarn, pip and Go.

**Cache mounts stay per environment, and previews start empty.** Sharing
the base environment's cache scope with its previews would warm their
first build, but cache mounts are mutable state, and a pull request's
code is untrusted (ADR 0056): it could plant a changed package store or
compiler cache that the next production build runs. Sharing one scope
among the previews of an environment has the same problem across pull
requests: a fork's could poison a preview that holds copied secrets.
Layers are content-addressed and shared by every build on a server, so a
preview's install step is still `CACHED` when its lockfile matches; only
the mounts start cold, once per pull request.

**The push says how long it took** (`Pushed REF (DIGEST) in 1.2s`), so a
slow push on a server shows in the build log instead of hiding between
the last BuildKit step and "Built".

## Consequences

The benchmark app, before (one core, gzip, tags) and after (two cores,
the default on a two-core server, zstd, pins), build and push:

| | Before | After |
|---|---|---|
| Dockerfile, cold | 59.8 s | 41.7 s |
| Dockerfile, warm (one-line change) | 35.4 s | 20.4 s |
| Dockerfile with the hinted cache mount, warm | 35.4 s without it | 15.8 s |
| Railpack, cold | 106.5 s | 65.3 s |
| Railpack, warm | 35.3 s | 15.9 s |

- What's left in a warm Dockerfile build is mostly the user's: here a
  `chmod -R` step that copies the app into a new layer (5.5 s), and
  TypeScript.
- Memory doesn't grow with the cap: Next.js sizes its worker pools by
  the host's cores, whatever the cgroup allows. `memory_mb` still caps
  a build, which fails saying so when it hits it.
- `TestBuildTimes` re-measures the build paths (pack, build, export,
  push, start; cold, then warm after a one-line change):
  `DOCK_BUILD_APP=$PWD/e2e/apps/nextjs-pnpm DOCK_BUILD_CPUS=2 go test
  -tags integration -run TestBuildTimes -v ./internal/node/build/`.

## Amendment (2026-11-17): BuildKit pushes, Railpack's images ahead

The two leftovers above. Measured as before (`TestBuildTimes`, two cores,
the same app), build and push:

| | Before | BuildKit pushes | And Railpack's images fetched ahead |
|---|---|---|---|
| Dockerfile, cold | 37.8 s | 40.3 s | |
| Dockerfile, warm | 20.2 s | 19.3 s | |
| Dockerfile with the cache mount, cold | 42.9 s | 43.2 s | |
| Dockerfile with the cache mount, warm | 15.1 s | 14.0 s | |
| Railpack, cold | 61.6 s | 61.9 s | 41.0 s (the fetch: 16.1 s, ahead) |
| Railpack, warm | 16.8 s | 13.2 s | |

The cold rows move with the build steps' own noise, ±2 s. The part
this changes, from the start of the export until the image is pushed
and in Docker:

| | Before | After |
|---|---|---|
| Dockerfile, cold | 2.6 s | 2.1 s |
| Dockerfile, warm | 2.2 s | 1.6 s |
| Railpack, cold | 10.9 s | 9.0 s |
| Railpack, warm | 3.2 s | 1.4 s |

**BuildKit pushes to the registry itself, and the builder pulls the
image back.** The Docker export sent the whole image as a tarball
through the daemon every build (2.2 s of a warm Railpack build), waited
for Docker to unpack it (up to 4.9 s cold), and then Docker pushed.
BuildKit's image exporter pushes only the layers the registry doesn't
have, and Docker's pull of the result fetches only the ones it doesn't
have, over loopback. The saving grows with the image. Layers stay zstd,
and BuildKit keeps no image record (`store=false`), which would hold
layers past the build cache's pruning.

The registry is on the host's loopback (the registry on the control
plane, the node link's tunnel on other servers), out of reach of the
BuildKit container on its own network. So a relay carries the push. For
the export only, the daemon serves a Unix socket in its data dir
(`builds/relay`), bind-mounted into the container, and joins each
connection to the registry. `dock-probe relay`, a mode of the probe
binary Dock already installs, runs in the container through the exec
API, listens on the registry's address in the container's own network
namespace and joins each connection to the socket. BuildKit pushes to
the same reference Docker did, plain HTTP on loopback. The push token
reaches BuildKit through the build's session (a token authority for
that one host); other registries stay anonymous. A builder without the
probe (a plain `go build`) loads into Docker and has Docker push, as
before.

**Security property: no build step reaches the registry.** Build steps
share the BuildKit container's network namespace, so whatever listens
there, a step could reach. The relay opens only after the build's last
step finished: the build function evaluates the result before the
export, starts the relay, and stops it when the export returns. Steps'
processes end with their step, so none is left to connect, or to listen
on the address first and catch the token.
`TestStepsDontReachTheRegistry` checks it from a step of a build that
pushes.

**A push keeps what it uploaded on disk until it ends** (amends 0084's
reads). With the bucket gateway, an object that finished uploading left
the spool at once, so the pull right after a build read the new layers
back from the bucket, slow or stuck as it may be. That was already true
of other servers' pulls, and with this change it would be true of the
builder's own. Now, while a push runs (`PushStarted`, which core holds
through the build and its pulls), an uploaded object keeps its spool
file. The file goes when the last push ends, or earlier when a write
needs the room. "What waits to upload" doesn't count those files.

**Builders fetch Railpack's images ahead, once per set of pins.** A
cold Railpack build spent about 20 s fetching the images its plan runs
on: the builder image (414 MB compressed) took 15 s, the runtime and
frontend the rest. The images are pinned (above), so they can be
fetched before any build needs them. Core asks a builder to fetch them
(`WarmBuilder`, protocol 32) whenever one may have become a builder:
the control plane at start, a node on connect, a server set to build.
The builder skips the fetch when a marker in its data dir names the
same pins. So it happens once on a new builder, and once after an
update that pins other images. The fetch reads each image's root
through BuildKit, which pulls and unpacks it as a build step would.

- **Builders only.** `[buildkit] enabled` is true on every server, app
  servers included, so a fetch at daemon start would put about 1.5 GB
  on servers that never build. Core knows which servers build.
- **The BuildKit container starts for the fetch** and keeps running,
  as after a first build: about 15 MB and no CPU while idle.
  (Amended 2026-11-26: it stops 5 minutes after its last use, a build,
  the fetch or a prune. After builds buildkitd held 314 MB on prod, and it
  answers again about 0.25 s after a start.)
- **Builds don't wait for it.** A build that starts meanwhile shares
  BuildKit's fetches.
- The images count toward the build cache. Pruning
  (`build_cache_keep_mb`) may remove them on a builder that only builds
  Dockerfiles, and then the next Railpack build fetches them, as before.
- No timer and no ticker: start, connect and the builder setting are
  the events. Shipping the images through the platform registry
  instead would make the control plane serve 460 MB to each new
  builder, from a bucket in bucket mode. That is no faster than
  ghcr.io, and it means another copy to keep in sync with the pins.
